"""Tests for nodeaudit/traffic-audit.py (the node-side host traffic collector)."""
import importlib.util
import json
import pathlib
import sqlite3
import tempfile
import time
import unittest
import zlib

ROOT = pathlib.Path(__file__).resolve().parent.parent
_spec = importlib.util.spec_from_file_location('traffic_audit', ROOT / 'nodeaudit' / 'traffic-audit.py')
ta = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(ta)


class ConfigTests(unittest.TestCase):
    def test_panel_labels_win_over_discovery(self):
        ports = ta.normalize_ports([
            {'proto': 'tcp', 'port': 443, 'label': 'vless-reality'},
            {'proto': 'tcp', 'port': 443, 'label': 'caddy'},
        ])
        self.assertEqual(ports, [{'proto': 'tcp', 'port': 443, 'label': 'vless_reality'}])

    def test_invalid_ports_dropped_and_capped(self):
        ports = ta.normalize_ports([{'proto': 'sctp', 'port': 1}, {'proto': 'tcp', 'port': 0},
                                    {'proto': 'tcp', 'port': 'x'}] +
                                   [{'proto': 'tcp', 'port': p} for p in range(1, 200)])
        self.assertEqual(len(ports), ta.MAX_PORTS)
        self.assertTrue(all(p['proto'] == 'tcp' and p['port'] >= 1 for p in ports))

    def test_label_is_nft_safe(self):
        self.assertEqual(ta.sanitize_label('Reality Upstream!!'), 'reality_upstream')
        self.assertEqual(ta.sanitize_label('$$$'), 'port')
        self.assertLessEqual(len(ta.sanitize_label('a' * 80)), 24)

    def test_load_config_rejects_bad_interface(self):
        with tempfile.TemporaryDirectory() as d:
            path = pathlib.Path(d) / 'c.json'
            path.write_text(json.dumps({'interface': 'eth0; rm -rf /'}))
            with self.assertRaises(RuntimeError):
                ta.load_config(path)

    def test_load_config_always_has_base_units(self):
        with tempfile.TemporaryDirectory() as d:
            config = ta.load_config(pathlib.Path(d) / 'missing.json')
        self.assertIn('sing-box.service', config['units'])
        self.assertIn('system.slice', config['units'])


class ListenerTests(unittest.TestCase):
    SS = '\n'.join([
        'tcp   LISTEN 0 4096   0.0.0.0:443      0.0.0.0:*  users:(("caddy",pid=811,fd=7))',
        'tcp   LISTEN 0 4096   127.0.0.1:2082   0.0.0.0:*  users:(("singbox-panel",pid=900,fd=3))',
        'udp   UNCONN 0 0      127.0.0.53%lo:53 0.0.0.0:*  users:(("systemd-resolve",pid=500,fd=13))',
        'udp   UNCONN 0 0      *:8443           *:*        users:(("sing-box",pid=1000,fd=9))',
        'udp   UNCONN 0 0      0.0.0.0:41641    0.0.0.0:*  users:(("tailscaled",pid=700,fd=5))',
        'udp   UNCONN 0 0      0.0.0.0:51234    0.0.0.0:*  users:(("chronyd",pid=650,fd=5))',
        'tcp   LISTEN 0 128    [::]:22          [::]:*     users:(("sshd",pid=600,fd=4))',
    ])

    def test_parse_listeners(self):
        got = {(p, port, proc) for p, port, proc, _ in ta.parse_listeners(self.SS, 32768)}
        self.assertIn(('tcp', 443, 'caddy'), got)
        self.assertIn(('udp', 8443, 'sing-box'), got)
        self.assertIn(('tcp', 22, 'sshd'), got)
        self.assertIn(('udp', 41641, 'tailscaled'), got)       # known fixed high port kept
        self.assertNotIn(('tcp', 2082, 'singbox-panel'), got)  # loopback skipped
        self.assertFalse(any(port == 53 for _, port, _ in got))
        self.assertFalse(any(port == 51234 for _, port, _ in got))  # ephemeral client socket


class RulesTests(unittest.TestCase):
    PORTS = [{'proto': 'tcp', 'port': 443, 'label': 'reality'}, {'proto': 'udp', 'port': 443, 'label': 'hy2'}]

    def test_layout_signature_changes_with_ports_and_device(self):
        a = ta.layout_signature('eth0', self.PORTS)
        self.assertEqual(a, ta.layout_signature('eth0', list(self.PORTS)))
        self.assertNotEqual(a, ta.layout_signature('ens3', self.PORTS))
        self.assertNotEqual(a, ta.layout_signature('eth0', self.PORTS[:1]))

    def test_rules_carry_layout_and_ownership_markers(self):
        rules = ta.initial_rules('eth0', 'g20261110', self.PORTS)
        self.assertIn('counter %s { }' % ta.OWNER_COUNTER, rules)
        self.assertIn('counter layout_%s { }' % ta.layout_signature('eth0', self.PORTS), rules)
        self.assertIn('comment "%s"' % ta.OWNER, rules)
        for direction in ('rx', 'tx'):
            self.assertIn('counter name port_tcp_443_reality_%s' % direction, rules)
            self.assertIn('counter name port_udp_443_hy2_%s' % direction, rules)
        # Counting only: never a drop/reject/nat statement.
        for word in ('drop', 'reject', 'dnat', 'snat', 'masquerade', 'policy drop'):
            self.assertNotIn(word, rules)

    def test_table_signature_reads_layout_counter(self):
        obj = {'nftables': [{'counter': {'name': 'layout_0123456789ab'}}, {'counter': {'name': 'wan_rx'}}]}
        self.assertEqual(ta.table_signature(obj), '0123456789ab')
        self.assertEqual(ta.table_signature({'nftables': []}), '')  # v1 table → rebuilt


class ClassifyTests(unittest.TestCase):
    def test_mapping(self):
        self.assertEqual(ta.classify('interface', 'rx'), ('interface', 'nic', 'rx'))
        self.assertEqual(ta.classify('service', 'sing-box.service:tx'), ('service', 'sing-box.service', 'tx'))
        self.assertEqual(ta.classify('nft', 'wan_tx'), ('wan', 'public', 'tx'))
        self.assertEqual(ta.classify('nft', 'peer_rx_recorded'), ('coverage', 'peer_recorded', 'rx'))
        self.assertEqual(ta.classify('nft', 'tcp_syn_in'), ('tcp', 'syn_in', 'rx'))
        self.assertEqual(ta.classify('nft', 'port_udp_8443_hysteria2_rx'), ('port', 'udp/8443 hysteria2', 'rx'))
        # v1 counters and markers never leak into the panel.
        self.assertIsNone(ta.classify('nft', ta.OWNER_COUNTER))
        self.assertIsNone(ta.classify('nft', 'layout_0123456789ab'))
        self.assertIsNone(ta.classify('peer', 'peer4_rx_g20261110:1.2.3.4'))


class HourlyTests(unittest.TestCase):
    """hourly() must emit only finished hours, sum by value, and include archives."""

    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.dir.name)
        db = ta.open_db(self.root)
        self.base = (int(time.time()) // 3600 - 5) * 3600   # five hours ago, aligned
        # Hour H: three minute samples of NIC rx; one is archived (compressed).
        h = self.base
        rows = [(h + 60, h, 'interface', 'rx', 1000, 10, 'ok'),
                (h + 120, h + 60, 'interface', 'rx', 2000, 20, 'ok'),
                (h + 120, h + 60, 'nft', 'port_tcp_443_reality_tx', 5000, 5, 'reset_lower_bound'),
                (h + 120, h + 60, 'service', 'caddy.service:rx', 70, 1, 'ok')]
        db.execute('INSERT INTO archives VALUES(?,?)',
                   (h + 60, zlib.compress(json.dumps([rows[0]]).encode())))
        for r in rows[1:]:
            db.execute('INSERT INTO samples VALUES(?,?,?,?,?,?,?)', r)
        # Hour H+1: still accumulating (last_sample inside it) → must not be emitted.
        db.execute('INSERT INTO samples VALUES(?,?,?,?,?,?,?)',
                   (h + 3600 + 60, h + 3600, 'interface', 'rx', 999, 9, 'ok'))
        for i in range(60):
            db.execute('INSERT INTO peers VALUES(?,?,?,?,?,?,?,?)',
                       (h, 'rx', 4, '198.51.100.%d' % i, 100 + i, 1, h, h + 60))
        db.execute("INSERT INTO meta VALUES('first_sample',?)", (str(h + 60),))
        db.execute("INSERT INTO meta VALUES('last_sample',?)", (str(h + 3600 + 60),))
        db.execute("INSERT INTO meta VALUES('interface','eth0')")
        db.commit()
        db.close()

    def tearDown(self):
        self.dir.cleanup()

    def test_only_finished_hours_with_exact_sums(self):
        out = ta.hourly(self.root, since=0)
        self.assertEqual([b['hour'] for b in out['hours']], [self.base])
        items = {(i['kind'], i['name']): i for i in out['hours'][0]['items']}
        nic = items[('interface', 'nic')]
        self.assertEqual((nic['rx'], nic['rx_packets'], nic['tx']), (3000, 30, 0))  # archive + live
        port = items[('port', 'tcp/443 reality')]
        self.assertEqual((port['tx'], port['resets']), (5000, 1))
        self.assertEqual(items[('service', 'caddy.service')]['rx'], 70)
        self.assertEqual(out['hours'][0]['samples'], 2)

    def test_peers_top_n_and_other(self):
        bucket = ta.hourly(self.root, since=0)['hours'][0]
        self.assertEqual(len(bucket['peers']['rx']), ta.PEERS_PER_HOUR)
        self.assertEqual(bucket['peers']['rx'][0], {'address': '198.51.100.59', 'bytes': 159, 'packets': 1})
        other = bucket['peers']['rx_other']
        self.assertEqual(other['count'], 60 - ta.PEERS_PER_HOUR)
        self.assertEqual(other['bytes'], sum(100 + i for i in range(60 - ta.PEERS_PER_HOUR)))

    def test_since_skips_already_pulled_hours(self):
        self.assertEqual(ta.hourly(self.root, since=self.base + 3600)['hours'], [])

    def test_repeat_pull_is_identical(self):
        self.assertEqual(ta.hourly(self.root, since=0)['hours'], ta.hourly(self.root, since=0)['hours'])


class DifferenceTests(unittest.TestCase):
    def test_reset_is_flagged_not_negative(self):
        delta, _, _, status = ta.difference(('e1', 5000, 50, 1.0), 'e1', 100, 1, 2.0)
        self.assertEqual((delta, status), (100, 'reset_lower_bound'))
        delta, _, _, status = ta.difference(('e1', 5000, 50, 1.0), 'e1', 5600, 56, 2.0)
        self.assertEqual((delta, status), (600, 'ok'))


if __name__ == '__main__':
    unittest.main()

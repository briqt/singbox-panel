import argparse
import unittest

from provision_relay import ensure_relay, normalized_sources, parse_entry


class RelayPanel:
    def __init__(self, relay=None, drift=False):
        self.relay = relay
        self.drift = drift
        self.writes = []

    def call(self, method, path, data=None, raw=False):
        if method == "GET" and path == "/api/nodes/8":
            return {"id": 8, "name": "laxcg", "inbounds": [self.relay] if self.relay else []}
        if method == "POST" and path == "/api/nodes/8/inbounds":
            self.writes.append((method, data))
            self.relay = {"id": 50, "tag": data["tag"], "protocol": data["protocol"], "port": data["port"], "settings": dict(data["settings"], method="m", password="k")}
            return self.relay
        if method == "PUT" and path == "/api/inbounds/50":
            self.writes.append((method, data))
            self.relay["settings"].update(data["settings"])
            return self.relay
        if path == "/api/nodes/8/generate":
            return {"inbounds": [{"tag": "relay"}]}
        if path == "/api/nodes/8/raw-config":
            return {"inbounds": [{"tag": "relay"}], "drift": True} if self.drift else {"inbounds": [{"tag": "relay"}]}
        raise AssertionError((method, path))


ARGS = argparse.Namespace(landing_node_id=8, relay_tag="relay", relay_port=8443)


class ProvisionRelayTest(unittest.TestCase):
    def test_sources_are_normalized_and_open_allowlist_rejected(self):
        self.assertEqual(normalized_sources(["203.0.113.4", "198.51.100.9/24"]), ["203.0.113.4/32", "198.51.100.0/24"])
        with self.assertRaises(ValueError):
            normalized_sources(["0.0.0.0/0"])

    def test_entry_format(self):
        self.assertEqual(parse_entry("1:2053:2053"), {"node_id": 1, "ports": {"hysteria2": 2053, "vless-reality": 2053}})
        with self.assertRaises(argparse.ArgumentTypeError):
            parse_entry("1:2053")

    def test_preview_never_writes(self):
        panel = RelayPanel()
        plan, relay = ensure_relay(panel, ARGS, ["203.0.113.4/32"], apply=False)
        self.assertEqual((plan["action"], relay, panel.writes), ("create", None, []))

    def test_rerun_reuses_and_allowlist_change_updates_without_new_key(self):
        panel = RelayPanel()
        ensure_relay(panel, ARGS, ["203.0.113.4/32"], apply=True)
        plan, _ = ensure_relay(panel, ARGS, ["203.0.113.4/32"], apply=True)
        self.assertEqual(plan["action"], "reuse")
        plan, relay = ensure_relay(panel, ARGS, ["203.0.113.5/32"], apply=True)
        self.assertEqual(plan["action"], "update-allowlist")
        self.assertEqual(relay["settings"]["password"], "k")
        self.assertNotIn("password", panel.writes[-1][1]["settings"])

    def test_conflicting_relay_is_refused(self):
        panel = RelayPanel(relay={"id": 50, "tag": "relay", "protocol": "shadowsocks", "port": 9999, "settings": {}})
        with self.assertRaises(RuntimeError):
            ensure_relay(panel, ARGS, ["203.0.113.4/32"], apply=False)

    def test_live_config_drift_fails(self):
        with self.assertRaises(RuntimeError):
            ensure_relay(RelayPanel(drift=True), ARGS, ["203.0.113.4/32"], apply=True)


if __name__ == "__main__":
    unittest.main()

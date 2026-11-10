#!/usr/bin/python3
"""节点整机流量计量（由 singbox-panel 安装与拉取）。

只做计数：独立的 nft 计数表 + systemd IPAccounting（--runtime，重启即失效）。
不抓包、不改过滤/NAT/路由、不重启任何业务服务。

数据分两层：
  - 本机 /var/lib/traffic-audit：分钟级原始增量，最多 48 小时、48 MiB，只作缓冲；
  - 面板：经 `hourly` 拉走按小时的结论，按面板自己的保留期存放。

兼容 Python 3.9+（Debian 11）与 nft 0.9.8+。
"""
import argparse
import datetime
import fcntl
import hashlib
import ipaddress
import json
import os
import pathlib
import re
import sqlite3
import subprocess
import sys
import time
import zlib

VERSION = '2'
ROOT = pathlib.Path('/var/lib/traffic-audit')
CONFIG = pathlib.Path('/etc/traffic-audit.json')
TABLE = 'traffic_audit'
OWNER = 'traffic-audit-v1'          # 表归属标记沿用 v1，升级时可识别并接管已有表
OWNER_COUNTER = 'owner_traffic_audit_v1'
RETENTION = 48 * 3600
DB_LIMIT = 48 * 1024 * 1024
MIN_FREE = 192 * 1024 * 1024        # 低于此值暂停采样，不影响业务
PEER_LIMIT = 1024                   # 每代次（UTC 日）每族每方向的来源上限
PEERS_PER_HOUR = 50                 # 每小时每方向上报给面板的来源条数，其余合并为 other
MAX_PORTS = 48
BASE_UNITS = (
    'sing-box.service', 'ssh.service', 'sshd.service', 'singbox-panel.service',
    'caddy.service', 'user.slice', 'system.slice',
)
KNOWN_HIGH_PORTS = {('udp', 41641)}  # tailscale 固定端口，落在临时端口区间也保留
LABEL_RE = re.compile(r'[^a-z0-9_]+')
SET_RE = re.compile(r'^peer([46])_(rx|tx)_(g\d{8})$')
PORT_RE = re.compile(r'^port_(tcp|udp)_(\d+)_([a-z0-9_]+)_(rx|tx)$')
LAYOUT_RE = re.compile(r'^layout_([0-9a-f]{12})$')


def run(args, stdin=None, check=True):
    result = subprocess.run(args, input=stdin, text=True, capture_output=True, timeout=12)
    if check and result.returncode:
        # 不回显原始命令、来源地址或可能包含配置的 stdout。
        raise RuntimeError('命令失败：' + args[0] + '：' + result.stderr[:400])
    return result


# ---------------------------------------------------------------- 配置

def sanitize_label(value):
    label = LABEL_RE.sub('_', str(value).lower()).strip('_')[:24].strip('_')
    return label or 'port'


def normalize_ports(items):
    """去重并校验端口表；先到先得，因此面板给的标签优先于自动发现的。"""
    seen, out = set(), []
    for item in items:
        proto = item.get('proto')
        try:
            port = int(item.get('port'))
        except (TypeError, ValueError):
            continue
        if proto not in ('tcp', 'udp') or not 1 <= port <= 65535 or (proto, port) in seen:
            continue
        seen.add((proto, port))
        out.append({'proto': proto, 'port': port, 'label': sanitize_label(item.get('label', 'port'))})
        if len(out) >= MAX_PORTS:
            break
    return out


def load_config(path=CONFIG):
    try:
        data = json.loads(path.read_text())
    except FileNotFoundError:
        data = {}
    ports = normalize_ports(data.get('ports') or [])
    units = []
    for unit in list(BASE_UNITS) + list(data.get('units') or []):
        if isinstance(unit, str) and re.fullmatch(r'[A-Za-z0-9@_.:\\-]+\.(service|slice|scope)', unit) and unit not in units:
            units.append(unit)
    iface = data.get('interface') or ''
    if iface and not re.fullmatch(r'[A-Za-z0-9_.:-]+', iface):
        raise RuntimeError('配置的接口名非法')
    return {'ports': ports, 'units': units, 'interface': iface}


def ephemeral_start():
    try:
        return int(pathlib.Path('/proc/sys/net/ipv4/ip_local_port_range').read_text().split()[0])
    except (OSError, ValueError, IndexError):
        return 32768


def _is_loopback(address):
    host = address.split('%', 1)[0].strip('[]')
    if host in ('*', '', '0.0.0.0', '::'):
        return False
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


SS_USERS_RE = re.compile(r'\("([^"]+)",pid=(\d+)')


def parse_listeners(output, ephemeral=32768):
    """解析 `ss -H -lntupn`：返回 [(proto, port, process, pid)]，跳过回环与临时端口。"""
    out = []
    for line in output.splitlines():
        fields = line.split()
        if len(fields) < 5:
            continue
        proto = fields[0].lower()
        if proto not in ('tcp', 'udp'):
            continue
        local = fields[4]
        if ':' not in local:
            continue
        address, _, port_text = local.rpartition(':')
        if not port_text.isdigit() or _is_loopback(address):
            continue
        port = int(port_text)
        if port >= ephemeral and (proto, port) not in KNOWN_HIGH_PORTS:
            continue
        match = SS_USERS_RE.search(line)
        process, pid = (match.group(1), int(match.group(2))) if match else ('', 0)
        out.append((proto, port, process, pid))
    return out


def unit_of_pid(pid):
    try:
        text = pathlib.Path('/proc/%d/cgroup' % pid).read_text()
    except OSError:
        return ''
    for line in text.splitlines():
        path = line.rsplit(':', 1)[-1]
        for part in reversed(path.split('/')):
            if part.endswith('.service'):
                return part
    return ''


def configure(desired):
    """合并面板下发的端口/服务与本机监听发现，原子写入配置并返回最终结果。"""
    ports = list(desired.get('ports') or [])
    units = list(desired.get('units') or [])
    listening = run(['ss', '-H', '-lntupn'], check=False).stdout
    discovered = []
    for proto, port, process, pid in sorted(parse_listeners(listening, ephemeral_start()), key=lambda x: (x[1], x[0])):
        discovered.append({'proto': proto, 'port': port, 'label': process or 'port'})
        unit = unit_of_pid(pid) if pid else ''
        if unit and unit not in units:
            units.append(unit)
    config = {
        'version': 2,
        'interface': desired.get('interface') or '',
        'ports': normalize_ports(ports + discovered),
        'units': units,
    }
    CONFIG.parent.mkdir(parents=True, exist_ok=True)
    tmp = CONFIG.with_suffix('.tmp')
    old = os.umask(0o077)
    try:
        tmp.write_text(json.dumps(config, ensure_ascii=False, indent=1))
    finally:
        os.umask(old)
    os.replace(tmp, CONFIG)
    return load_config()


# ---------------------------------------------------------------- 网卡

def default_devices():
    names = set()
    for family in ('-4', '-6'):
        result = run(['ip', '-j', family, 'route', 'show', 'default'], check=False)
        if result.returncode:
            continue
        try:
            routes = json.loads(result.stdout or '[]')
        except ValueError:
            continue
        names.update(r['dev'] for r in routes if r.get('dev'))
    return sorted(names)


def interface(config):
    if config.get('interface'):
        dev = config['interface']
        if not (pathlib.Path('/sys/class/net') / dev).exists():
            raise RuntimeError('配置的公网接口不存在')
        return dev
    # 主表 IPv4 与 IPv6 默认路由的并集。de 这类 IPv6-only + WARP 策略路由的节点，
    # IPv4 主表没有默认路由，只看 IPv4 会误判为"没有公网接口"。
    names = default_devices()
    if len(names) != 1 or not re.fullmatch(r'[A-Za-z0-9_.:-]+', names[0]):
        raise RuntimeError('公网默认接口不唯一（%s），请在面板指定接口' % (','.join(names) or '无'))
    return names[0]


# ---------------------------------------------------------------- nft

def layout_signature(dev, ports):
    raw = json.dumps({'dev': dev, 'ports': ports}, sort_keys=True).encode()
    return hashlib.sha256(raw).hexdigest()[:12]


def nft_json():
    result = run(['nft', '-j', '-n', 'list', 'table', 'inet', TABLE], check=False)
    if result.returncode:
        if 'No such file or directory' in result.stderr:
            return None
        raise RuntimeError('无法读取独立计数表，拒绝覆盖')
    obj = json.loads(result.stdout)
    tables = [x['table'] for x in obj['nftables'] if 'table' in x]
    # 部分 nft 版本的 JSON 会丢弃 table comment；使用命名标记和规则注释双重核对。
    marker = any(x.get('counter', {}).get('name') == OWNER_COUNTER for x in obj['nftables'])
    owned_rule = any(x.get('rule', {}).get('comment') == OWNER for x in obj['nftables'])
    if len(tables) != 1 or not marker or not owned_rule:
        raise RuntimeError('同名 nft 表不属于本工具，拒绝操作')
    return obj


def set_name(family, direction, generation):
    return 'peer%s_%s_%s' % (family, direction, generation)


def peer_rule(family, direction, generation, dev):
    chain, field, address = ('ingress', 'iifname', 'saddr') if direction == 'rx' else ('egress', 'oifname', 'daddr')
    proto = 'ip' if family == 4 else 'ip6'
    nfproto = 'ipv4' if family == 4 else 'ipv6'
    name = set_name(family, direction, generation)
    # set 满时 update 会结束本条规则，后面的 recorded 计数器不会增加。
    rule = ('%s "%s" meta nfproto %s update @%s { %s %s counter } '
            'counter name peer_%s_recorded comment "peer%s_%s:%s"'
            % (field, dev, nfproto, name, proto, address, direction, family, direction, generation))
    return chain, rule


def set_declaration(family, direction, generation):
    typ = 'ipv4_addr' if family == 4 else 'ipv6_addr'
    return 'set %s { type %s; flags dynamic; size %d; }' % (set_name(family, direction, generation), typ, PEER_LIMIT)


def port_counter(port, direction):
    return 'port_%s_%d_%s_%s' % (port['proto'], port['port'], port['label'], direction)


def initial_rules(dev, generation, ports):
    signature = layout_signature(dev, ports)
    lines = ['table inet %s {' % TABLE]
    for family in (4, 6):
        for direction in ('rx', 'tx'):
            lines.append(set_declaration(family, direction, generation))
    counters = [OWNER_COUNTER, 'layout_' + signature, 'wan_rx', 'wan_tx', 'peer_rx_recorded',
                'peer_tx_recorded', 'tcp_syn_in', 'tcp_rst_in', 'tcp_synack_out']
    for port in ports:
        for direction in ('rx', 'tx'):
            counters.append(port_counter(port, direction))
    lines += ['counter %s { }' % name for name in counters]
    for direction, chain, hook, priority, field, port_field in (
        ('rx', 'ingress', 'prerouting', -310, 'iifname', 'dport'),
        ('tx', 'egress', 'postrouting', 300, 'oifname', 'sport'),
    ):
        lines += ['chain %s { type filter hook %s priority %d; policy accept;' % (chain, hook, priority),
                  '%s "%s" counter name wan_%s comment "%s"' % (field, dev, direction, OWNER)]
        for family in (4, 6):
            lines.append(peer_rule(family, direction, generation, dev)[1])
        if direction == 'rx':
            lines += ['%s "%s" tcp flags & (syn | ack) == syn counter name tcp_syn_in' % (field, dev),
                      '%s "%s" tcp flags & rst == rst counter name tcp_rst_in' % (field, dev)]
        else:
            lines.append('%s "%s" tcp flags & (syn | ack) == (syn | ack) counter name tcp_synack_out' % (field, dev))
        for port in ports:
            lines.append('%s "%s" %s %s %d counter name %s'
                         % (field, dev, port['proto'], port_field, port['port'], port_counter(port, direction)))
        lines.append('}')
    lines.append('}')
    return '\n'.join(lines) + '\n'


def table_signature(obj):
    for item in obj['nftables']:
        match = LAYOUT_RE.fullmatch(item.get('counter', {}).get('name', ''))
        if match:
            return match.group(1)
    return ''


def ensure_table(dev, generation, ports, db=None, now=None):
    obj = nft_json()
    if obj is not None and table_signature(obj) != layout_signature(dev, ports):
        # 端口布局或接口变了（含 v1 升级）：整表重建。计数从零开始，save_counter 会因
        # 表 handle 变化把这一段标成 reset_lower_bound，不会把旧值当增量。
        run(['nft', 'delete', 'table', 'inet', TABLE])
        if db is not None:
            db.execute('INSERT INTO events VALUES(?,?,?)', (now or time.time(), 'layout_changed', dev))
        obj = None
    if obj is None:
        rules = initial_rules(dev, generation, ports)
        run(['nft', '--check', '-f', '-'], rules)
        run(['nft', '-f', '-'], rules)
        return nft_json(), True
    peer_rules = {}
    for item in obj['nftables']:
        rule = item.get('rule', {})
        match = re.fullmatch(r'peer([46])_(rx|tx):(g\d{8})', rule.get('comment', ''))
        if match:
            peer_rules[(int(match.group(1)), match.group(2))] = (rule, match.group(3))
    if len(peer_rules) != 4:
        raise RuntimeError('来源计数规则不完整，拒绝修改其他规则')
    generations = {value[1] for value in peer_rules.values()}
    if len(generations) != 1:
        raise RuntimeError('来源计数代次不一致')
    if generations == {generation}:
        return obj, False
    # 原子切换到新一日的空 set。旧 set 保留到其最后计数入库后才删除，避免读取/清空竞态。
    lines = []
    for family in (4, 6):
        for direction in ('rx', 'tx'):
            name = set_name(family, direction, generation)
            if any(x.get('set', {}).get('name') == name for x in obj['nftables']):
                raise RuntimeError('新代次 set 已存在，拒绝盲目复用')
            lines.append('add ' + set_declaration(family, direction, generation).replace('set ', 'set inet ' + TABLE + ' ', 1))
            old, _ = peer_rules[(family, direction)]
            chain, rule = peer_rule(family, direction, generation, dev)
            lines.append('replace rule inet %s %s handle %s %s' % (TABLE, chain, old['handle'], rule))
    batch = '\n'.join(lines) + '\n'
    run(['nft', '--check', '-f', '-'], batch)
    run(['nft', '-f', '-'], batch)
    return nft_json(), False


# ---------------------------------------------------------------- 存储

def open_db(root=ROOT, readonly=False):
    path = root / 'audit.sqlite3'
    if readonly:
        return sqlite3.connect(path.as_uri() + '?mode=ro', uri=True, timeout=8)
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    db = sqlite3.connect(path, timeout=8)
    os.chmod(path, 0o600)
    db.execute('PRAGMA journal_mode=DELETE')
    db.execute('PRAGMA synchronous=FULL')
    db.execute('PRAGMA cache_size=-2048')
    db.execute('PRAGMA journal_size_limit=0')
    page_size = db.execute('PRAGMA page_size').fetchone()[0]
    db.execute('PRAGMA max_page_count=%d' % (DB_LIMIT // page_size))
    db.executescript('''
    CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);
    CREATE TABLE IF NOT EXISTS baseline(
      key TEXT PRIMARY KEY,epoch TEXT NOT NULL,value INTEGER NOT NULL,
      packets INTEGER NOT NULL,ts REAL NOT NULL);
    CREATE TABLE IF NOT EXISTS samples(
      ts REAL NOT NULL,start REAL NOT NULL,kind TEXT NOT NULL,name TEXT NOT NULL,
      bytes INTEGER NOT NULL,packets INTEGER NOT NULL,status TEXT NOT NULL,
      PRIMARY KEY(ts,kind,name));
    CREATE INDEX IF NOT EXISTS samples_time ON samples(ts);
    CREATE TABLE IF NOT EXISTS archives(ts REAL PRIMARY KEY,payload BLOB NOT NULL);
    CREATE TABLE IF NOT EXISTS reads(ts REAL PRIMARY KEY,started REAL NOT NULL,finished REAL NOT NULL,before TEXT NOT NULL,after TEXT NOT NULL);
    CREATE TABLE IF NOT EXISTS peers(
      hour INTEGER NOT NULL,direction TEXT NOT NULL,family INTEGER NOT NULL,
      address TEXT NOT NULL,bytes INTEGER NOT NULL,packets INTEGER NOT NULL,
      first REAL NOT NULL,last REAL NOT NULL,
      PRIMARY KEY(hour,direction,family,address));
    CREATE TABLE IF NOT EXISTS events(
      ts REAL NOT NULL,kind TEXT NOT NULL,detail TEXT NOT NULL);
    CREATE TABLE IF NOT EXISTS service_state(
      unit TEXT PRIMARY KEY,original_accounting TEXT NOT NULL,last_pid TEXT NOT NULL,
      last_epoch TEXT NOT NULL,last_seen REAL NOT NULL);
    ''')
    return db


def difference(previous, epoch, value, packets, ts, initial_zero=False):
    if previous is None:
        return (value if initial_zero else 0, packets if initial_zero else 0,
                ts, 'new' if initial_zero else 'baseline')
    old_epoch, old_value, old_packets, old_ts = previous
    if old_epoch != epoch or value < old_value or packets < old_packets:
        # 可以证明的仅是新计数器当前值，重置前最后一分钟尾巴无法恢复，必须标明。
        return value, packets, old_ts, 'reset_lower_bound'
    return value - old_value, packets - old_packets, old_ts, 'ok'


def save_counter(db, ts, kind, name, epoch, value, packets=0, initial_zero=False, event=True):
    key = kind + ':' + name
    previous = db.execute('SELECT epoch,value,packets,ts FROM baseline WHERE key=?', (key,)).fetchone()
    delta, delta_packets, start, status = difference(previous, epoch, value, packets, ts, initial_zero)
    db.execute('INSERT OR REPLACE INTO baseline VALUES(?,?,?,?,?)', (key, epoch, value, packets, ts))
    if event:
        db.execute('INSERT INTO samples VALUES(?,?,?,?,?,?,?)', (ts, start, kind, name, delta, delta_packets, status))
    return delta, delta_packets, start, status


def service_properties(units):
    props = ('Id', 'LoadState', 'MainPID', 'InvocationID', 'ActiveState', 'IPAccounting',
             'IPIngressBytes', 'IPEgressBytes', 'IPIngressPackets', 'IPEgressPackets', 'ControlGroup')
    result = run(['systemctl', 'show'] + list(units) + ['--property=' + p for p in props])
    records, seen = [], set()
    for block in result.stdout.strip().split('\n\n'):
        fields = dict(line.split('=', 1) for line in block.splitlines() if '=' in line)
        # ssh.service 与 sshd.service 在 Debian 系是同一单元的别名，按 Id 去重。
        if fields.get('Id') and fields.get('LoadState') == 'loaded' and fields['Id'] not in seen:
            seen.add(fields['Id'])
            records.append(fields)
    return records


def capture_services(db, now, boot, units):
    records = service_properties(units)
    changed = False
    for item in records:
        unit = item['Id']
        if item.get('ActiveState') != 'active':
            continue
        old = db.execute('SELECT original_accounting FROM service_state WHERE unit=?', (unit,)).fetchone()
        if old is None:
            db.execute('INSERT INTO service_state VALUES(?,?,?,?,?)',
                       (unit, item.get('IPAccounting', 'no'), '', '', now))
            # 先提交原始开关，避免 set-property 成功而回滚时丢失卸载依据。
            db.commit()
        if item.get('IPAccounting') != 'yes':
            run(['systemctl', 'set-property', '--runtime', unit, 'IPAccounting=yes'])
            changed = True
    if changed:
        records = service_properties(units)
    for item in records:
        unit = item['Id']
        if item.get('ActiveState') != 'active':
            continue
        rx, tx = item.get('IPIngressBytes', ''), item.get('IPEgressBytes', '')
        if not rx.isdigit() or not tx.isdigit():
            db.execute('INSERT INTO events VALUES(?,?,?)', (now, 'accounting_unavailable', unit))
            continue
        cgroup = pathlib.Path('/sys/fs/cgroup') / item.get('ControlGroup', '').lstrip('/')
        try:
            inode = str(cgroup.stat().st_ino)
        except OSError:
            inode = 'missing'
        epoch = boot + ':' + item.get('InvocationID', '') + ':' + inode
        for direction, value, packet_key in (('rx', rx, 'IPIngressPackets'), ('tx', tx, 'IPEgressPackets')):
            packets = item.get(packet_key, '0')
            save_counter(db, now, 'service', unit + ':' + direction, epoch,
                         int(value), int(packets) if packets.isdigit() else 0)
        db.execute('UPDATE service_state SET last_pid=?,last_epoch=?,last_seen=? WHERE unit=?',
                   (item.get('MainPID', '0'), epoch, now, unit))


def extract_peers(obj):
    for item in obj['nftables']:
        data = item.get('set', {})
        match = SET_RE.fullmatch(data.get('name', ''))
        if not match:
            continue
        for entry in data.get('elem', []):
            value = entry.get('elem', entry) if isinstance(entry, dict) else {}
            counter = value.get('counter', {})
            address = value.get('val')
            if not address or not counter:
                raise RuntimeError('来源元素缺少实际字节计数')
            address = str(ipaddress.ip_address(address))
            yield data['name'], int(match.group(1)), match.group(2), address, int(counter['bytes']), int(counter['packets'])


def compact(db, now):
    # 两小时前的分钟明细无损压缩；字节值、采样区间、换代标志全部保留。
    batch = {}
    for row in db.execute('SELECT * FROM samples WHERE ts<? ORDER BY ts', (now - 7200,)):
        batch.setdefault(row[0], []).append(row)
    for ts, rows in batch.items():
        payload = zlib.compress(json.dumps(rows, separators=(',', ':')).encode(), 3)
        db.execute('INSERT OR REPLACE INTO archives VALUES(?,?)', (ts, payload))
        db.execute('DELETE FROM samples WHERE ts=?', (ts,))


def prune(db, now):
    cutoff = now - RETENTION
    db.execute('DELETE FROM samples WHERE ts<?', (cutoff,))
    db.execute('DELETE FROM archives WHERE ts<?', (cutoff,))
    db.execute('DELETE FROM reads WHERE ts<?', (cutoff,))
    db.execute('DELETE FROM peers WHERE hour<?', (cutoff,))
    db.execute('DELETE FROM events WHERE ts<?', (cutoff,))
    # baseline 是仍存活计数器的游标而非历史；停采后仍须靠它补读，不能跟历史一起删。


def collect(root=ROOT, config_path=CONFIG):
    os.umask(0o077)
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    lock = (root / 'collect.lock').open('a')
    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    stat = os.statvfs(root)
    if stat.f_bavail * stat.f_frsize < MIN_FREE:
        raise RuntimeError('可用磁盘低于192MiB，暂停采样，不影响业务')
    config = load_config(config_path)
    now = time.time()
    generation = datetime.datetime.fromtimestamp(now, datetime.timezone.utc).strftime('g%Y%m%d')
    boot = pathlib.Path('/proc/sys/kernel/random/boot_id').read_text().strip()
    dev = interface(config)
    db = open_db(root)
    prune(db, now)
    db.commit()
    read_started = time.time()
    before_obj, created = ensure_table(dev, generation, config['ports'], db, now)
    obj = nft_json()
    after_obj = nft_json()
    read_finished = time.time()

    def read_points(data):
        return {x['counter']['name']: int(x['counter']['bytes']) for x in data['nftables']
                if 'counter' in x and x['counter']['name'] in ('wan_rx', 'wan_tx', 'peer_rx_recorded', 'peer_tx_recorded')}
    db.execute('INSERT INTO reads VALUES(?,?,?,?,?)',
               (now, read_started, read_finished, json.dumps(read_points(before_obj)), json.dumps(read_points(after_obj))))
    table_info = next(x['table'] for x in obj['nftables'] if 'table' in x)
    table_epoch = boot + ':' + str(table_info['handle'])
    first = db.execute("SELECT value FROM meta WHERE key='first_sample'").fetchone()
    db.execute("INSERT OR IGNORE INTO meta VALUES('first_sample',?)", (str(now),))
    db.execute("INSERT OR REPLACE INTO meta VALUES('last_sample',?)", (str(now),))
    db.execute("INSERT OR REPLACE INTO meta VALUES('interface',?)", (dev,))
    db.execute("INSERT OR REPLACE INTO meta VALUES('version',?)", (VERSION,))
    for item in after_obj['nftables']:
        counter = item.get('counter')
        if counter and counter['name'] != OWNER_COUNTER and not LAYOUT_RE.fullmatch(counter['name']):
            save_counter(db, now, 'nft', counter['name'], table_epoch + ':' + str(counter.get('handle')),
                         int(counter['bytes']), int(counter['packets']))
    for name, family, direction, address, value, packets in extract_peers(obj):
        delta, dp, start, status = save_counter(db, now, 'peer', name + ':' + address, table_epoch,
                                                value, packets, initial_zero=bool(first), event=False)
        if status == 'reset_lower_bound':
            db.execute('INSERT INTO events VALUES(?,?,?)', (now, 'source_counter_reset', name))
        if delta or dp:
            db.execute('''INSERT INTO peers VALUES(?,?,?,?,?,?,?,?)
                ON CONFLICT(hour,direction,family,address) DO UPDATE SET
                bytes=bytes+excluded.bytes,packets=packets+excluded.packets,last=excluded.last''',
                       (int(now // 3600) * 3600, direction, family, address, delta, dp, start, now))
    for direction in ('rx', 'tx'):
        path = pathlib.Path('/sys/class/net') / dev / 'statistics'
        save_counter(db, now, 'interface', direction, boot + ':' + dev,
                     int((path / (direction + '_bytes')).read_text()),
                     int((path / (direction + '_packets')).read_text()))
    db.commit()
    capture_services(db, now, boot, config['units'])
    prune(db, now)
    compact(db, now)
    db.commit()
    # 删除已经完整入库、且不再被任何规则引用的旧 set；不清空正在使用的 set。
    for item in obj['nftables']:
        data = item.get('set', {})
        match = SET_RE.fullmatch(data.get('name', ''))
        if match and match.group(3) != generation:
            run(['nft', 'delete', 'set', 'inet', TABLE, data['name']])
            db.execute('DELETE FROM baseline WHERE key LIKE ?', ('peer:' + data['name'] + ':%',))
    db.commit()
    db.close()
    try:
        (root / 'last-error.json').unlink()
    except FileNotFoundError:
        pass
    return {'ok': True, 'ts': now, 'interface': dev, 'new_table': created}


# ---------------------------------------------------------------- 读取

def iter_rows(db, since, until):
    """返回 [since, until) 内全部分钟增量行：(ts,start,kind,name,bytes,packets,status)。"""
    for row in db.execute('SELECT ts,start,kind,name,bytes,packets,status FROM samples WHERE ts>=? AND ts<?', (since, until)):
        yield tuple(row)
    for _, payload in db.execute('SELECT ts,payload FROM archives WHERE ts>=? AND ts<?', (since, until)):
        for row in json.loads(zlib.decompress(payload)):
            yield tuple(row)


def classify(kind, name):
    """把内部计数名映射成面板口径 (kind, name, direction)；不对外的返回 None。"""
    if kind == 'interface' and name in ('rx', 'tx'):
        return 'interface', 'nic', name
    if kind == 'service' and ':' in name:
        unit, direction = name.rsplit(':', 1)
        if direction in ('rx', 'tx'):
            return 'service', unit, direction
        return None
    if kind != 'nft':
        return None
    if name in ('wan_rx', 'wan_tx'):
        return 'wan', 'public', name[-2:]
    if name in ('peer_rx_recorded', 'peer_tx_recorded'):
        return 'coverage', 'peer_recorded', name[5:7]
    if name == 'tcp_syn_in':
        return 'tcp', 'syn_in', 'rx'
    if name == 'tcp_rst_in':
        return 'tcp', 'rst_in', 'rx'
    if name == 'tcp_synack_out':
        return 'tcp', 'synack_out', 'tx'
    match = PORT_RE.fullmatch(name)
    if match:
        return 'port', '%s/%s %s' % (match.group(1), match.group(2), match.group(3)), match.group(4)
    return None


def hourly(root=ROOT, since=0.0):
    """按小时输出结论。只输出已经结束的小时（之后已有采样），因此面板可以幂等覆盖写入。"""
    db = open_db(root, readonly=True)
    meta = dict(db.execute('SELECT key,value FROM meta'))
    if 'last_sample' not in meta:
        return {'version': VERSION, 'hours': []}
    last = float(meta['last_sample'])
    first = float(meta['first_sample'])
    start = max(int(since // 3600) * 3600, int(first // 3600) * 3600, int((time.time() - RETENTION) // 3600) * 3600)
    end = int(last // 3600) * 3600          # 不含：最后一个小时仍在累积
    hours = {}
    for ts, begin, kind, name, value, packets, status in iter_rows(db, start, end):
        key = classify(kind, name)
        hour = int(ts // 3600) * 3600
        bucket = hours.setdefault(hour, {'hour': hour, 'samples': set(), 'items': {}})
        bucket['samples'].add(ts)
        if key is None:
            continue
        kind2, name2, direction = key
        item = bucket['items'].setdefault((kind2, name2), {
            'kind': kind2, 'name': name2, 'rx': 0, 'tx': 0, 'rx_packets': 0, 'tx_packets': 0, 'resets': 0})
        item[direction] += value
        item[direction + '_packets'] += packets
        item['resets'] += int(status == 'reset_lower_bound')
    out = []
    for hour in sorted(hours):
        bucket = hours[hour]
        stamps = sorted(bucket['samples'])
        gaps = [b - a for a, b in zip(stamps, stamps[1:])]
        peers = {}
        for direction in ('rx', 'tx'):
            rows = db.execute('''SELECT address,SUM(bytes),SUM(packets) FROM peers WHERE hour=? AND direction=?
                GROUP BY address ORDER BY SUM(bytes) DESC''', (hour, direction)).fetchall()
            top = rows[:PEERS_PER_HOUR]
            rest = rows[PEERS_PER_HOUR:]
            peers[direction] = [{'address': r[0], 'bytes': r[1], 'packets': r[2]} for r in top]
            peers[direction + '_other'] = {'count': len(rest), 'bytes': sum(r[1] for r in rest),
                                           'packets': sum(r[2] for r in rest)}
        out.append({
            'hour': hour,
            'samples': len(stamps),
            'first_ts': stamps[0] if stamps else None,
            'last_ts': stamps[-1] if stamps else None,
            'max_gap': max(gaps) if gaps else 0,
            'items': sorted(bucket['items'].values(), key=lambda x: (x['kind'], x['name'])),
            'peers': peers,
        })
    events = db.execute('SELECT kind,COUNT(*) FROM events WHERE ts>=? GROUP BY kind', (start,)).fetchall()
    db.close()
    return {'version': VERSION, 'interface': meta.get('interface', ''), 'first_sample': first,
            'last_sample': last, 'hours': out, 'events': dict(events)}


def status(root=ROOT, config_path=CONFIG):
    out = {'version': VERSION, 'installed': True, 'now': time.time()}
    timer = run(['systemctl', 'is-active', 'traffic-audit.timer'], check=False).stdout.strip()
    out['timer'] = timer
    try:
        out['config'] = load_config(config_path)
    except Exception as error:  # noqa: BLE001 - 状态接口要把错误带回面板
        out['config_error'] = str(error)[:300]
    path = root / 'audit.sqlite3'
    if path.exists():
        db = open_db(root, readonly=True)
        meta = dict(db.execute('SELECT key,value FROM meta'))
        db.close()
        for key in ('first_sample', 'last_sample'):
            if key in meta:
                out[key] = float(meta[key])
        out['interface'] = meta.get('interface', '')
        if 'last_sample' in out:
            out['last_sample_age'] = out['now'] - out['last_sample']
    out['storage_bytes'] = sum(p.stat().st_size for p in root.iterdir() if p.is_file()) if root.exists() else 0
    error_path = root / 'last-error.json'
    if error_path.exists():
        try:
            out['last_error'] = json.loads(error_path.read_text())
        except ValueError:
            out['last_error'] = {'error': 'unreadable'}
    out['limits'] = {'retention_hours': RETENTION // 3600, 'db_max_bytes': DB_LIMIT, 'min_free_bytes': MIN_FREE}
    return out


def uninstall(root=ROOT):
    """恢复被打开的 IPAccounting 开关并删除本工具的计数表；文件由调用方删除。"""
    restored = []
    path = root / 'audit.sqlite3'
    if path.exists():
        db = sqlite3.connect(path, timeout=8)
        try:
            for unit, original in db.execute('SELECT unit,original_accounting FROM service_state'):
                if original != 'yes':
                    run(['systemctl', 'set-property', '--runtime', unit, 'IPAccounting=no'], check=False)
                    restored.append(unit)
        finally:
            db.close()
    removed = False
    if nft_json() is not None:   # 归属核对失败会抛错，不会删别人的表
        run(['nft', 'delete', 'table', 'inet', TABLE])
        removed = True
    return {'ok': True, 'accounting_restored': restored, 'table_removed': removed}


def report(root=ROOT, hours=24):
    """人读的近况汇总（兼容 v1 命令）：把 hourly 结论合并成区间总量。"""
    data = hourly(root, time.time() - hours * 3600)
    totals = {}
    for bucket in data['hours']:
        for item in bucket['items']:
            entry = totals.setdefault((item['kind'], item['name']), {'kind': item['kind'], 'name': item['name'], 'rx': 0, 'tx': 0})
            entry['rx'] += item['rx']
            entry['tx'] += item['tx']
    data['totals'] = sorted(totals.values(), key=lambda x: -(x['rx'] + x['tx']))
    return data


def main(argv=None):
    parser = argparse.ArgumentParser(description='轻量、受限的主机流量计量')
    parser.add_argument('command', choices=('collect', 'hourly', 'status', 'configure', 'uninstall', 'report', 'version'))
    parser.add_argument('--since', type=float, default=0.0)
    parser.add_argument('--hours', type=int, default=24)
    args = parser.parse_args(argv)
    if args.command == 'collect':
        collect()          # 成功不输出，失败写 last-error.json 并以非零退出（进 journal）
        return
    if args.command == 'version':
        result = {'version': VERSION}
    elif args.command == 'hourly':
        result = hourly(since=args.since)
    elif args.command == 'status':
        result = status()
    elif args.command == 'configure':
        result = configure(json.loads(sys.stdin.read() or '{}'))
    elif args.command == 'uninstall':
        result = uninstall()
    else:
        result = report(hours=max(1, min(48, args.hours)))
    print(json.dumps(result, ensure_ascii=False, separators=(',', ':')))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:  # noqa: BLE001
        if len(sys.argv) > 1 and sys.argv[1] == 'collect':
            os.umask(0o077)
            ROOT.mkdir(mode=0o700, parents=True, exist_ok=True)
            tmp = ROOT / 'last-error.tmp'
            tmp.write_text(json.dumps({'ts': time.time(), 'error': type(error).__name__,
                                       'detail': str(error)[:1000]}, ensure_ascii=False))
            os.replace(tmp, ROOT / 'last-error.json')
        print('traffic-audit: %s: %s' % (type(error).__name__, str(error)[:500]), file=sys.stderr)
        raise SystemExit(1)

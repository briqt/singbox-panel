import argparse
import unittest

from provision_relay import desired_order, ensure_lines, ensure_relay, normalized_sources, parse_entry, same_outbound


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


class EntryPanel:
    def __init__(self, inbounds):
        self.inbounds = inbounds
        self.writes = []

    def call(self, method, path, data=None, raw=False):
        if method == "GET" and path == "/api/nodes/2":
            return {"id": 2, "name": "laxpro", "inbounds": [dict(item) for item in self.inbounds]}
        if method == "POST" and path == "/api/nodes/2/inbounds":
            self.writes.append(("create", data["tag"]))
            item = dict(data, id=100 + len(self.inbounds), sort_order=0)
            self.inbounds.append(item)
            return item
        if method == "PUT" and path == "/api/nodes/2/inbounds/reorder":
            self.writes.append(("reorder", [entry["id"] for entry in data]))
            order = {entry["id"]: entry["sort_order"] for entry in data}
            for item in self.inbounds:
                item["sort_order"] = order[item["id"]]
            return {}
        if path in ("/api/nodes/2/generate", "/api/nodes/2/raw-config"):
            return {"inbounds": [{"tag": item["tag"]} for item in self.inbounds]}
        if "/users" in path or "/access" in path:
            raise AssertionError("脚本不得触碰授权：" + path)
        raise AssertionError((method, path))


OUT = {"type": "shadowsocks", "server": "198.51.100.7", "server_port": 8443, "method": "m", "password": "k"}
LINES = [{"name": "laxpro-Hy2-laxcg", "tag": "hysteria2-laxcg", "protocol": "hysteria2", "port": 8443, "outbound": OUT},
         {"name": "laxpro-Reality-laxcg", "tag": "vless-reality-laxcg", "protocol": "vless-reality", "port": 8443, "outbound": OUT}]


def original():
    return [{"id": 27, "tag": "hysteria2", "name": "", "protocol": "hysteria2", "port": 443, "sort_order": 0},
            {"id": 28, "tag": "vless-reality", "name": "", "protocol": "vless-reality", "port": 443, "sort_order": 0}]


class EntryLinesTest(unittest.TestCase):
    def test_new_lines_go_after_originals_in_fixed_order(self):
        inbounds = original() + [{"id": 45, "tag": "vless-reality-laxcg", "sort_order": 0}, {"id": 44, "tag": "hysteria2-laxcg", "sort_order": 0}]
        inbounds[0]["sort_order"] = 0
        self.assertEqual(desired_order(inbounds, ["hysteria2-laxcg", "vless-reality-laxcg"]), [27, 28, 44, 45])

    def test_apply_creates_reorders_and_never_touches_access(self):
        panel = EntryPanel(original()[:1] + [{"id": 44, "tag": "hysteria2-laxcg", "name": "laxpro-Hy2-laxcg", "protocol": "hysteria2", "port": 8443, "outbound": dict(OUT), "sort_order": 0}] + original()[1:])
        panel.inbounds[2]["sort_order"] = 5
        results, extra = ensure_lines(panel, 2, LINES, apply=True)
        self.assertEqual([item["action"] for item in results], ["reuse", "create"])
        self.assertTrue(extra["reorder"])
        order = [item["id"] for item in sorted(panel.inbounds, key=lambda item: (item["sort_order"], item["id"]))]
        self.assertEqual([item["tag"] for item in sorted(panel.inbounds, key=lambda item: (item["sort_order"], item["id"]))],
                         ["hysteria2", "vless-reality", "hysteria2-laxcg", "vless-reality-laxcg"], order)
        # 再跑一次：全部复用，不再写入。
        panel.writes.clear()
        results, extra = ensure_lines(panel, 2, LINES, apply=True)
        self.assertEqual(([item["action"] for item in results], extra["reorder"], panel.writes), (["reuse", "reuse"], False, []))

    def test_preview_never_writes(self):
        panel = EntryPanel(original())
        results, _ = ensure_lines(panel, 2, LINES, apply=False)
        self.assertEqual(([item["action"] for item in results], panel.writes), (["create", "create"], []))

    def test_existing_line_with_different_config_is_refused(self):
        panel = EntryPanel(original() + [{"id": 44, "tag": "hysteria2-laxcg", "name": "other", "protocol": "hysteria2", "port": 8443, "outbound": dict(OUT)}])
        with self.assertRaises(RuntimeError):
            ensure_lines(panel, 2, LINES, apply=False)

    def test_outbound_network_empty_equals_missing(self):
        self.assertTrue(same_outbound(dict(OUT), dict(OUT, network="")))
        self.assertFalse(same_outbound(dict(OUT), dict(OUT, network="tcp")))


if __name__ == "__main__":
    unittest.main()

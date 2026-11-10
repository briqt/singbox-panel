import base64
import copy
import unittest

from provision_upstream import provision


class FakePanel:
    def __init__(self):
        self.inbounds = [{"id": 11, "node_id": 1, "tag": "original", "protocol": "vless-reality", "name": "", "port": 8443}]
        self.access = [11]
        self.calls = []
        self.fail_grant = False
        self.other_drift = False

    def config(self, node):
        if node == 2:
            return {"inbounds": [], "stable": not (self.other_drift and 12 in self.access)}
        return {"inbounds": [{"tag": item["tag"], "port": item["port"]} for item in self.inbounds if item["id"] in self.access]}

    def call(self, method, path, data=None, raw=False):
        self.calls.append((method, path, copy.deepcopy(data)))
        if method == "GET" and path == "/api/nodes/1":
            return {"id": 1, "inbounds": copy.deepcopy(self.inbounds)}
        if method == "GET" and path == "/api/users/7":
            return {"id": 7, "sub_token": "test-token"}
        if method == "GET" and path == "/api/users/7/access":
            return [copy.deepcopy(item) for item in self.inbounds if item["id"] in self.access]
        if method == "GET" and path == "/api/nodes":
            return [{"id": ident, "enabled": True, "proxy_type": "singbox"} for ident in (1, 2)]
        if method == "POST" and path.endswith("/generate"):
            return self.config(int(path.split("/")[3]))
        if method == "GET" and path.endswith("/raw-config"):
            return self.config(1)
        if method == "GET" and path.startswith("/sub/"):
            lines = ["original-uri"] + (["new-uri"] if 12 in self.access else [])
            content = "\n".join(lines).encode()
            return content if "format=clash" in path else base64.b64encode(content)
        if method == "POST" and path == "/api/nodes/1/inbounds":
            inbound = dict(data, id=12, node_id=1)
            self.inbounds.append(inbound)
            return copy.deepcopy(inbound)
        if method == "PUT" and path == "/api/users/7":
            if self.fail_grant and 12 in data["inbound_ids"]:
                raise RuntimeError("simulated API failure")
            self.access = list(data["inbound_ids"])
            return {"sync": [{"node_id": 1, "status": "pushed"}]}
        if method == "DELETE" and path == "/api/inbounds/12":
            self.inbounds = [item for item in self.inbounds if item["id"] != 12]
            return {}
        raise AssertionError((method, path))


class ProvisionTests(unittest.TestCase):
    def setUp(self):
        self.panel = FakePanel()
        self.request = {"name": "test-upstream", "tag": "new", "protocol": "vless-reality", "port": 2053, "outbound": {"type": "socks", "server": "192.0.2.1", "server_port": 443, "username": "example", "password": "example-password", "network": "tcp"}}

    def test_preview_never_mutates(self):
        result = provision(self.panel, 1, 7, self.request)
        self.assertFalse(result["apply"])
        self.assertTrue(all(method == "GET" for method, _, _ in self.panel.calls))

    def test_apply_preserves_old_and_is_idempotent(self):
        result = provision(self.panel, 1, 7, self.request, True)
        self.assertEqual(result["inbound_id"], 12)
        self.assertTrue(result["other_nodes_unchanged"])
        self.assertEqual(self.panel.access, [11, 12])
        self.panel.calls.clear()
        provision(self.panel, 1, 7, self.request, True)
        self.assertFalse(any(method in ("PUT", "DELETE") or path.endswith("/inbounds") and method == "POST" for method, path, _ in self.panel.calls))

    def test_failed_grant_removes_new_line(self):
        self.panel.fail_grant = True
        with self.assertRaises(RuntimeError):
            provision(self.panel, 1, 7, self.request, True)
        self.assertEqual(self.panel.access, [11])
        self.assertEqual([item["id"] for item in self.panel.inbounds], [11])

    def test_other_node_drift_triggers_full_rollback(self):
        self.panel.other_drift = True
        with self.assertRaisesRegex(RuntimeError, "其他节点"):
            provision(self.panel, 1, 7, self.request, True)
        self.assertEqual(self.panel.access, [11])
        self.assertEqual([item["id"] for item in self.panel.inbounds], [11])


if __name__ == "__main__":
    unittest.main()

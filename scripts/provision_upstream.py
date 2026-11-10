#!/usr/bin/env python3
"""经面板原生 API 增量配置并授权上游线路；默认只读预览，不写 JWT 文件。"""

import argparse
import base64
import json
import os
import sys
import urllib.error
import urllib.request
from pathlib import Path


class Panel:
    def __init__(self, url, username, password):
        self.url = url.rstrip("/")
        self.token = None
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        self.token = self.call("POST", "/api/login", {"username": username, "password": password})["token"]

    def call(self, method, path, data=None, raw=False):
        headers = {"Content-Type": "application/json"}
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        request = urllib.request.Request(self.url + path, data=json.dumps(data).encode() if data is not None else None, headers=headers, method=method)
        try:
            with self.opener.open(request, timeout=90) as response:
                content = response.read()
        except urllib.error.HTTPError as error:
            # 不把令牌、请求体、认证信息或远端原始错误写进日志。
            operation = "subscription" if path.startswith("/sub/") else path
            raise RuntimeError(f"{method} {operation}: HTTP {error.code}") from None
        return content if raw else (json.loads(content) if content else None)


def env_settings(path):
    result = dict(os.environ)
    if path:
        for line in Path(path).read_text().splitlines():
            line = line.strip()
            if line and not line.startswith("#") and "=" in line:
                key, value = line.split("=", 1)
                result[key.strip()] = value.strip().strip("\"'")
    return result


def subscription_lines(data):
    return base64.b64decode(data, validate=False).decode().splitlines()


def provision(panel, node_id, user_id, requested, apply=False):
    node = panel.call("GET", f"/api/nodes/{node_id}")
    user = panel.call("GET", f"/api/users/{user_id}")
    access = panel.call("GET", f"/api/users/{user_id}/access") or []
    before_ids = sorted(item["id"] for item in access)
    matches = [item for item in node.get("inbounds", []) if item["tag"] == requested["tag"]]
    if len(matches) > 1:
        raise RuntimeError("存在重复线路 tag，拒绝有歧义的配置")
    existing = matches[0] if matches else None
    if existing:
        for key in ("name", "protocol", "port", "outbound"):
            actual = existing.get(key)
            expected = requested.get(key)
            if key == "outbound":
                actual = dict(actual or {})
                expected = dict(expected or {})
                actual.setdefault("network", "tcp")
                expected.setdefault("network", "tcp")
            if actual != expected:
                raise RuntimeError("同 tag 线路已存在但配置不同，请用编辑线路 API 明确修改")
    summary = {"node_id": node_id, "user_id": user_id, "name": requested["name"], "port": requested["port"], "action": "reuse" if existing else "create", "apply": apply}
    if not apply:
        return summary

    nodes = panel.call("GET", "/api/nodes") or []
    before_configs = {item["id"]: panel.call("POST", f"/api/nodes/{item['id']}/generate", {}) for item in nodes if item["enabled"] and item["proxy_type"] == "singbox"}
    sub_path = "/sub/" + user["sub_token"]
    before_sub = subscription_lines(panel.call("GET", sub_path, raw=True))
    created = None
    changed_access = False
    try:
        inbound = existing
        if inbound is None:
            inbound = panel.call("POST", f"/api/nodes/{node_id}/inbounds", requested)
            created = inbound["id"]
        target_ids = sorted(set(before_ids + [inbound["id"]]))
        if target_ids != before_ids:
            response = panel.call("PUT", f"/api/users/{user_id}", {"inbound_ids": target_ids})
            changed_access = True
            sync = response.get("sync", [])
            if any(item["node_id"] != node_id for item in sync):
                raise RuntimeError("授权变更不应同步其他节点")
        after_access = panel.call("GET", f"/api/users/{user_id}/access") or []
        if sorted(item["id"] for item in after_access) != target_ids:
            raise RuntimeError("回读授权与预期不符")
        after_sub = subscription_lines(panel.call("GET", sub_path, raw=True))
        expected_additions = 0 if inbound["id"] in before_ids else 1
        if len(after_sub) != len(before_sub) + expected_additions or not set(before_sub).issubset(after_sub):
            raise RuntimeError("已有订阅条目发生变化")
        after_configs = {ident: panel.call("POST", f"/api/nodes/{ident}/generate", {}) for ident in before_configs}
        for ident, before in before_configs.items():
            after = after_configs[ident]
            if ident != node_id and after != before:
                raise RuntimeError("其他节点生成配置发生变化")
            if ident == node_id:
                indexed = {item["tag"]: item for item in after.get("inbounds", [])}
                if any(indexed.get(item["tag"]) != item for item in before.get("inbounds", [])):
                    raise RuntimeError("目标节点原有入站被修改")
        live = panel.call("GET", f"/api/nodes/{node_id}/raw-config")
        if live != after_configs[node_id]:
            raise RuntimeError("远端配置与面板生成结果不一致")
        for fmt in ("base64", "clash"):
            content = panel.call("GET", sub_path + "?format=" + fmt, raw=True)
            if fmt == "base64":
                content = base64.b64decode(content)
            secret = requested.get("outbound", {}).get("password", "")
            if secret and secret.encode() in content:
                raise RuntimeError("订阅出现服务端上游凭据")
        summary.update({"inbound_id": inbound["id"], "old_entries_unchanged": True, "other_nodes_unchanged": True, "remote_config_verified": True})
        return summary
    except Exception:
        rollback_errors = []
        if changed_access:
            try:
                panel.call("PUT", f"/api/users/{user_id}", {"inbound_ids": before_ids})
            except Exception:
                rollback_errors.append("授权恢复失败")
        if created is not None:
            try:
                panel.call("DELETE", f"/api/inbounds/{created}")
            except Exception:
                rollback_errors.append("新增线路清理失败")
        if rollback_errors:
            raise RuntimeError("配置失败且回滚不完整：" + "、".join(rollback_errors)) from None
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", default="http://127.0.0.1:2082")
    parser.add_argument("--env-file", help="读取 ADMIN_USER / ADMIN_PASS，不输出凭据")
    parser.add_argument("--node-id", type=int, required=True)
    parser.add_argument("--user-id", type=int, required=True)
    parser.add_argument("--name", required=True)
    parser.add_argument("--tag", default="reality-upstream")
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    # 出口 JSON 从 stdin 输入，避免密码出现在 argv、仓库或临时 JWT 文件。
    outbound = json.load(sys.stdin)
    settings = env_settings(args.env_file)
    panel = Panel(args.url, settings.get("ADMIN_USER", "admin"), settings["ADMIN_PASS"])
    requested = {"name": args.name, "tag": args.tag, "protocol": "vless-reality", "port": args.port, "outbound": outbound}
    result = provision(panel, args.node_id, args.user_id, requested, args.apply)
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(json.dumps({"error": str(error)}, ensure_ascii=False), file=sys.stderr)
        raise SystemExit(1)

#!/usr/bin/env python3
"""经面板 API 配置 SS2022 落地：一台落地入站 + 每台入口机各一条 hysteria2 / vless-reality 落地线路。

默认只读预览；--apply 才写入。可重复执行：已存在且一致的线路复用，不重建密钥；
配置不一致时拒绝，交给操作者用编辑接口明确修改。落地入站的密钥只在面板与节点间流转，
本脚本不打印任何密钥、UUID 或 JWT。
"""

import argparse
import ipaddress
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from provision_upstream import Panel, env_settings, provision  # noqa: E402

PROTOCOLS = {"hysteria2": "Hy2", "vless-reality": "Reality"}


def parse_entry(text):
    try:
        node_id, hy2_port, reality_port = (int(part) for part in text.split(":"))
    except ValueError:
        raise argparse.ArgumentTypeError("入口格式为 节点ID:hysteria2端口:reality端口") from None
    return {"node_id": node_id, "ports": {"hysteria2": hy2_port, "vless-reality": reality_port}}


def normalized_sources(sources):
    result = []
    for source in sources:
        network = ipaddress.ip_network(source, strict=False)
        if network.prefixlen == 0:
            raise ValueError("白名单不能放行全部地址")
        result.append(str(network))
    return result


def settings_of(inbound):
    settings = inbound.get("settings") or {}
    return json.loads(settings) if isinstance(settings, str) else settings


def ensure_relay(panel, args, sources, apply):
    landing = panel.call("GET", f"/api/nodes/{args.landing_node_id}")
    matches = [item for item in landing.get("inbounds", []) if item["tag"] == args.relay_tag]
    if len(matches) > 1:
        raise RuntimeError("落地节点存在重复 tag，拒绝有歧义的配置")
    relay = matches[0] if matches else None
    if relay and (relay["protocol"] != "shadowsocks" or relay["port"] != args.relay_port):
        raise RuntimeError("同 tag 的落地入站已存在但协议或端口不同，请先明确处理")
    plan = {"landing": landing["name"], "relay_port": args.relay_port, "allowed_sources": sources}
    if relay is None:
        plan["action"] = "create"
    elif normalized_sources(settings_of(relay).get("allowed_sources") or []) != sources:
        plan["action"] = "update-allowlist"
    else:
        plan["action"] = "reuse"
    if not apply:
        return plan, None
    if plan["action"] == "create":
        relay = panel.call("POST", f"/api/nodes/{args.landing_node_id}/inbounds", {
            "name": f"{landing['name']}-relay", "tag": args.relay_tag, "protocol": "shadowsocks",
            "port": args.relay_port, "settings": {"allowed_sources": sources},
        })
    elif plan["action"] == "update-allowlist":
        relay = panel.call("PUT", f"/api/inbounds/{relay['id']}", {"settings": {"allowed_sources": sources}})
    # 判据落在节点上实际运行的配置，不落在接口返回 200。
    generated = panel.call("POST", f"/api/nodes/{args.landing_node_id}/generate", {})
    live = panel.call("GET", f"/api/nodes/{args.landing_node_id}/raw-config")
    if live != generated:
        raise RuntimeError("落地节点的运行配置与面板生成结果不一致")
    tags = {item.get("tag") for item in live.get("inbounds", [])}
    if args.relay_tag not in tags:
        raise RuntimeError("落地入站没有出现在节点运行配置里")
    relay = panel.call("GET", f"/api/nodes/{args.landing_node_id}")
    relay = next(item for item in relay["inbounds"] if item["tag"] == args.relay_tag)
    plan["inbound_id"] = relay["id"]
    return plan, relay


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--url", default="http://127.0.0.1:2082")
    parser.add_argument("--env-file", help="读取 ADMIN_USER / ADMIN_PASS，不输出凭据")
    parser.add_argument("--landing-node-id", type=int, required=True)
    parser.add_argument("--landing-server", required=True, help="入口机连接落地用的 IP；写 IP 避免出口依赖 DNS")
    parser.add_argument("--relay-port", type=int, required=True)
    parser.add_argument("--relay-tag", default="relay")
    parser.add_argument("--suffix", required=True, help="线路 tag 与名称后缀，如 laxcg")
    parser.add_argument("--entry", type=parse_entry, action="append", required=True, help="节点ID:hysteria2端口:reality端口，可重复")
    parser.add_argument("--source", action="append", required=True, help="落地白名单：入口机连落地时的源 IP/CIDR，可重复")
    parser.add_argument("--user-id", type=int, action="append", required=True, help="获授权的用户，可重复")
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    ipaddress.ip_address(args.landing_server)
    sources = normalized_sources(args.source)
    settings = env_settings(args.env_file)
    panel = Panel(args.url, settings.get("ADMIN_USER", "admin"), settings["ADMIN_PASS"])

    relay_plan, relay = ensure_relay(panel, args, sources, args.apply)
    report = {"relay": relay_plan, "lines": [], "apply": args.apply}
    if relay is not None:
        relay_settings = settings_of(relay)
        outbound = {"type": "shadowsocks", "server": args.landing_server, "server_port": args.relay_port,
                    "method": relay_settings["method"], "password": relay_settings["password"]}
    else:
        # 预览阶段落地可能尚不存在；比对时只看名称、协议与端口。
        outbound = None
    for entry in args.entry:
        node = panel.call("GET", f"/api/nodes/{entry['node_id']}")
        for protocol, label in PROTOCOLS.items():
            requested = {"name": f"{node['name']}-{label}-{args.suffix}", "tag": f"{protocol}-{args.suffix}",
                         "protocol": protocol, "port": entry["ports"][protocol]}
            for user_id in args.user_id:
                if outbound is None:
                    existing = [item for item in node.get("inbounds", []) if item["tag"] == requested["tag"]]
                    report["lines"].append({**requested, "node_id": node["id"], "user_id": user_id, "action": "reuse" if existing else "create", "apply": False})
                    continue
                result = provision(panel, node["id"], user_id, {**requested, "outbound": outbound}, args.apply)
                report["lines"].append({**requested, **result})
    print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(json.dumps({"error": str(error)}, ensure_ascii=False), file=sys.stderr)
        raise SystemExit(1)

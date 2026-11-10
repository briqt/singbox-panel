#!/usr/bin/env python3
"""经面板 API 配置 SS2022 落地：一台落地入站 + 每台入口机各一条 hysteria2 / vless-reality 落地线路。

默认只读预览；--apply 才写入。可重复执行：已存在且一致的线路复用，不重建密钥；
配置不一致时拒绝，交给操作者用编辑接口明确修改。新线路排在该节点原有线路之后。
本脚本不碰任何用户授权——新线路在授权前不监听，授权由操作者在面板里决定。
落地入站的密钥只在面板与节点间流转，本脚本不打印任何密钥、UUID 或 JWT。
"""

import argparse
import ipaddress
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from provision_upstream import Panel, env_settings  # noqa: E402

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


def same_outbound(actual, expected):
    actual, expected = dict(actual or {}), dict(expected or {})
    for item in (actual, expected):
        if item.get("network") in ("", None):
            item.pop("network", None)
    return actual == expected


def desired_order(inbounds, suffix_tags):
    """原有线路保持相对顺序在前，本脚本管理的线路按 suffix_tags 顺序排在最后。"""
    managed = {tag: index for index, tag in enumerate(suffix_tags)}
    ordered = sorted(inbounds, key=lambda item: (item.get("sort_order", 0), item["id"]))
    head = [item for item in ordered if item["tag"] not in managed]
    tail = sorted((item for item in ordered if item["tag"] in managed), key=lambda item: managed[item["tag"]])
    return [item["id"] for item in head + tail]


def ensure_lines(panel, node_id, lines, apply):
    """在一台入口机上建好落地线路并排序；返回每条线路的动作与校验结果。"""
    node = panel.call("GET", f"/api/nodes/{node_id}")
    results = []
    for requested in lines:
        matches = [item for item in node.get("inbounds", []) if item["tag"] == requested["tag"]]
        if len(matches) > 1:
            raise RuntimeError(f"{node['name']} 存在重复 tag {requested['tag']}")
        existing = matches[0] if matches else None
        if existing is not None:
            differs = [key for key in ("name", "protocol", "port") if existing.get(key) != requested[key]]
            if requested.get("outbound") is not None and not same_outbound(existing.get("outbound"), requested["outbound"]):
                differs.append("outbound")
            if differs:
                raise RuntimeError(f"{node['name']} 的 {requested['tag']} 已存在但 {','.join(differs)} 不同，请用编辑接口明确修改")
        results.append({"node": node["name"], "name": requested["name"], "tag": requested["tag"], "port": requested["port"],
                        "action": "reuse" if existing else "create", "inbound_id": existing["id"] if existing else None})
    tags = [line["tag"] for line in lines]
    if not apply:
        current = [item["id"] for item in sorted(node.get("inbounds", []), key=lambda item: (item.get("sort_order", 0), item["id"]))]
        ready = all(result["action"] == "reuse" for result in results)
        return results, {"reorder": ready and current != desired_order(node.get("inbounds", []), tags)}

    before = panel.call("POST", f"/api/nodes/{node_id}/generate", {})
    for requested, result in zip(lines, results):
        if result["action"] == "create":
            created = panel.call("POST", f"/api/nodes/{node_id}/inbounds", requested)
            result["inbound_id"] = created["id"]
    node = panel.call("GET", f"/api/nodes/{node_id}")
    current = [item["id"] for item in sorted(node["inbounds"], key=lambda item: (item.get("sort_order", 0), item["id"]))]
    wanted = desired_order(node["inbounds"], tags)
    reordered = current != wanted
    if reordered:
        panel.call("PUT", f"/api/nodes/{node_id}/inbounds/reorder", [{"id": ident, "sort_order": index} for index, ident in enumerate(wanted)])
    node = panel.call("GET", f"/api/nodes/{node_id}")
    after_order = [item["id"] for item in sorted(node["inbounds"], key=lambda item: (item.get("sort_order", 0), item["id"]))]
    if after_order != wanted:
        raise RuntimeError(f"{node['name']} 回读的线路顺序与预期不符")
    after = panel.call("POST", f"/api/nodes/{node_id}/generate", {})
    live = panel.call("GET", f"/api/nodes/{node_id}/raw-config")
    if live != after:
        raise RuntimeError(f"{node['name']} 的运行配置与面板生成结果不一致")
    # 原有线路的服务端配置必须原样保留（顺序变化不算）。
    old = {item["tag"]: item for item in before.get("inbounds", []) if item.get("tag") not in tags}
    new = {item["tag"]: item for item in after.get("inbounds", []) if item.get("tag") not in tags}
    if old != new:
        raise RuntimeError(f"{node['name']} 原有入站的配置被改动")
    for result in results:
        result.update({"remote_config_verified": True, "old_inbounds_unchanged": True})
    return results, {"reorder": reordered}


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
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    ipaddress.ip_address(args.landing_server)
    sources = normalized_sources(args.source)
    settings = env_settings(args.env_file)
    panel = Panel(args.url, settings.get("ADMIN_USER", "admin"), settings["ADMIN_PASS"])

    relay_plan, relay = ensure_relay(panel, args, sources, args.apply)
    report = {"relay": relay_plan, "lines": [], "nodes": {}, "apply": args.apply}
    if relay is not None:
        relay_settings = settings_of(relay)
        outbound = {"type": "shadowsocks", "server": args.landing_server, "server_port": args.relay_port,
                    "method": relay_settings["method"], "password": relay_settings["password"]}
    else:
        # 预览阶段落地可能尚不存在；比对时只看名称、协议与端口。
        outbound = None
    for entry in args.entry:
        node = panel.call("GET", f"/api/nodes/{entry['node_id']}")
        # 名称与订阅一致：节点名-协议缩写-后缀，如 laxpro-Hy2-laxcg。
        lines = [{"name": f"{node['name']}-{label}-{args.suffix}", "tag": f"{protocol}-{args.suffix}",
                  "protocol": protocol, "port": entry["ports"][protocol], "outbound": outbound}
                 for protocol, label in PROTOCOLS.items()]
        results, extra = ensure_lines(panel, entry["node_id"], lines, args.apply)
        report["lines"].extend(results)
        report["nodes"][node["name"]] = extra
    print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(json.dumps({"error": str(error)}, ensure_ascii=False), file=sys.stderr)
        raise SystemExit(1)

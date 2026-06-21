package singbox

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/briqt/singbox-panel/model"
)

func GenerateClashConfig(user model.User, nodes []model.NodeWithInbounds) string {
	var proxies []string
	var proxyNames []string

	for _, n := range nodes {
		if !n.Enabled {
			continue
		}
		for _, ib := range n.Inbounds {
			if !ib.Enabled {
				continue
			}
			entry := buildClashProxy(user, n.Node, ib)
			if entry != "" {
				proxies = append(proxies, entry)
				name := clashProxyName(n.Node, ib)
				proxyNames = append(proxyNames, name)
			}
		}
	}

	var sb strings.Builder
	sb.WriteString("mixed-port: 7890\n")
	sb.WriteString("allow-lan: false\n")
	sb.WriteString("mode: rule\n")
	sb.WriteString("log-level: info\n")
	sb.WriteString("unified-delay: true\n")
	sb.WriteString("find-process-mode: strict\n")
	sb.WriteString("dns:\n  enable: true\n  enhanced-mode: fake-ip\n  nameserver:\n    - https://dns.alidns.com/dns-query\n    - https://doh.pub/dns-query\n")
	sb.WriteString("\nproxies:\n")
	for _, p := range proxies {
		sb.WriteString(p)
		sb.WriteString("\n")
	}

	sb.WriteString("\nproxy-groups:\n")
	sb.WriteString("  - name: Proxy\n    type: select\n    proxies:\n      - Auto\n")
	for _, name := range proxyNames {
		sb.WriteString(fmt.Sprintf("      - %s\n", name))
	}
	sb.WriteString("      - DIRECT\n")
	sb.WriteString("  - name: Auto\n    type: url-test\n    proxies:\n")
	for _, name := range proxyNames {
		sb.WriteString(fmt.Sprintf("      - %s\n", name))
	}
	sb.WriteString("    url: https://www.gstatic.com/generate_204\n    interval: 300\n")

	sb.WriteString("\nrules:\n")
	sb.WriteString("  - GEOIP,CN,DIRECT\n")
	sb.WriteString("  - MATCH,Proxy\n")

	return sb.String()
}

func clashProxyName(node model.Node, ib model.NodeInbound) string {
	suffix := ""
	switch ib.Protocol {
	case "vless-vision":
		suffix = "Vision"
	case "hysteria2":
		suffix = "Hy2"
	case "vless-reality":
		suffix = "Reality"
	}
	return fmt.Sprintf("%s-%s", node.Name, suffix)
}

func buildClashProxy(user model.User, node model.Node, ib model.NodeInbound) string {
	var settings map[string]any
	json.Unmarshal(ib.Settings, &settings)
	if settings == nil {
		settings = map[string]any{}
	}

	switch ib.Protocol {
	case "vless-vision":
		return buildClashVLESSVision(user, node, ib, settings)
	case "hysteria2":
		return buildClashHysteria2(user, node, ib, settings)
	case "vless-reality":
		return buildClashVLESSReality(user, node, ib, settings)
	}
	return ""
}

func buildClashVLESSVision(user model.User, node model.Node, ib model.NodeInbound, settings map[string]any) string {
	domain, _ := settings["tls_domain"].(string)
	if domain == "" {
		domain = node.Domain
	}
	if domain == "" {
		return ""
	}
	name := clashProxyName(node, ib)
	return fmt.Sprintf(`  - name: "%s"
    type: vless
    server: %s
    port: %d
    udp: true
    uuid: %s
    flow: xtls-rprx-vision
    tls: true
    servername: %s
    client-fingerprint: chrome
    skip-cert-verify: false
    network: tcp`, name, node.Host, ib.Port, user.UUID, domain)
}

func buildClashHysteria2(user model.User, node model.Node, ib model.NodeInbound, settings map[string]any) string {
	domain, _ := settings["tls_domain"].(string)
	if domain == "" {
		domain = node.Domain
	}
	if domain == "" {
		return ""
	}
	name := clashProxyName(node, ib)
	return fmt.Sprintf(`  - name: "%s"
    type: hysteria2
    server: %s
    port: %d
    password: %s
    sni: %s
    alpn:
      - h3`, name, node.Host, ib.Port, user.UUID, domain)
}

func buildClashVLESSReality(user model.User, node model.Node, ib model.NodeInbound, settings map[string]any) string {
	sni, _ := settings["reality_sni"].(string)
	publicKey, _ := settings["reality_public_key"].(string)
	shortID, _ := settings["reality_short_id"].(string)
	if sni == "" || publicKey == "" {
		return ""
	}
	fp, _ := settings["reality_fingerprint"].(string)
	if fp == "" {
		fp = "chrome"
	}
	name := clashProxyName(node, ib)
	return fmt.Sprintf(`  - name: "%s"
    type: vless
    server: %s
    port: %d
    udp: true
    uuid: %s
    flow: xtls-rprx-vision
    tls: true
    servername: %s
    client-fingerprint: %s
    reality-opts:
      public-key: %s
      short-id: %s
    network: tcp`, name, node.Host, ib.Port, user.UUID, sni, fp, publicKey, shortID)
}

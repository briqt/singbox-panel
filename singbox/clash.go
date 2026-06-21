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
				proxyNames = append(proxyNames, clashName(n.Node, ib))
			}
		}
	}

	var sb strings.Builder
	sb.WriteString("mixed-port: 7890\nallow-lan: false\nmode: rule\nlog-level: info\nunified-delay: true\n")
	sb.WriteString("dns:\n  enable: true\n  enhanced-mode: fake-ip\n  nameserver:\n    - https://dns.alidns.com/dns-query\n    - https://doh.pub/dns-query\n")
	sb.WriteString("\nproxies:\n")
	for _, p := range proxies {
		sb.WriteString(p + "\n")
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
	sb.WriteString("\nrules:\n  - GEOIP,CN,DIRECT\n  - MATCH,Proxy\n")
	return sb.String()
}

func clashName(node model.Node, ib model.NodeInbound) string {
	switch ib.Protocol {
	case "hysteria2":
		return node.Name + "-Hy2"
	case "vless-reality":
		return node.Name + "-Reality"
	case "vless-httpupgrade":
		return node.Name + "-CDN"
	}
	return node.Name
}

func buildClashProxy(user model.User, node model.Node, ib model.NodeInbound) string {
	var s map[string]any
	json.Unmarshal(ib.Settings, &s)
	if s == nil {
		s = map[string]any{}
	}
	switch ib.Protocol {
	case "hysteria2":
		return clashHysteria2(user, node, ib, s)
	case "vless-reality":
		return clashReality(user, node, ib, s)
	case "vless-httpupgrade":
		return clashHTTPUpgrade(user, node, ib, s)
	}
	return ""
}

func clashHysteria2(user model.User, node model.Node, ib model.NodeInbound, s map[string]any) string {
	domain, _ := s["domain"].(string)
	if domain == "" {
		domain = node.Domain
	}
	if domain == "" {
		return ""
	}
	return fmt.Sprintf(`  - name: "%s-Hy2"
    type: hysteria2
    server: %s
    port: %d
    password: %s
    sni: %s
    alpn:
      - h3`, node.Name, node.Host, ib.Port, user.UUID, domain)
}

func clashReality(user model.User, node model.Node, ib model.NodeInbound, s map[string]any) string {
	sni, _ := s["sni"].(string)
	publicKey, _ := s["public_key"].(string)
	shortID, _ := s["short_id"].(string)
	fp, _ := s["fingerprint"].(string)
	if fp == "" {
		fp = "chrome"
	}
	if sni == "" || publicKey == "" {
		return ""
	}
	return fmt.Sprintf(`  - name: "%s-Reality"
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
    network: tcp`, node.Name, node.Host, ib.Port, user.UUID, sni, fp, publicKey, shortID)
}

func clashHTTPUpgrade(user model.User, node model.Node, ib model.NodeInbound, s map[string]any) string {
	domain, _ := s["domain"].(string)
	if domain == "" {
		domain = node.Domain
	}
	if domain == "" {
		return ""
	}
	path, _ := s["path"].(string)
	if path == "" {
		path = "/upgrade"
	}
	return fmt.Sprintf(`  - name: "%s-CDN"
    type: vless
    server: %s
    port: %d
    udp: true
    uuid: %s
    tls: true
    servername: %s
    client-fingerprint: chrome
    network: httpupgrade
    httpupgrade-opts:
      path: %s
      host: %s`, node.Name, domain, ib.Port, user.UUID, domain, path, domain)
}

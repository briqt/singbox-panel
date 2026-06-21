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
		names := protocolNameTracker{}
		for _, ib := range n.Inbounds {
			if !ib.Enabled {
				continue
			}
			name := clashName(n.Node, ib, names)
			entry := buildClashProxy(user, n.Node, ib, name)
			if entry != "" {
				proxies = append(proxies, entry)
				proxyNames = append(proxyNames, name)
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

func clashName(node model.Node, ib model.NodeInbound, names protocolNameTracker) string {
	switch ib.Protocol {
	case "hysteria2":
		return names.next(node.Name + "-Hy2")
	case "vless-reality":
		return names.next(node.Name + "-Reality")
	case "vless-httpupgrade":
		return names.next(node.Name + "-CDN")
	}
	return names.next(node.Name)
}

func buildClashProxy(user model.User, node model.Node, ib model.NodeInbound, name string) string {
	var s map[string]any
	json.Unmarshal(ib.Settings, &s)
	if s == nil {
		s = map[string]any{}
	}
	switch ib.Protocol {
	case "hysteria2":
		return clashHysteria2(user, node, ib, s, name)
	case "vless-reality":
		return clashReality(user, node, ib, s, name)
	case "vless-httpupgrade":
		return clashHTTPUpgrade(user, node, ib, s, name)
	}
	return ""
}

func clashHysteria2(user model.User, node model.Node, ib model.NodeInbound, s map[string]any, name string) string {
	domain, _ := s["domain"].(string)
	if domain == "" {
		domain = node.Domain
	}
	if domain == "" {
		return ""
	}
	return fmt.Sprintf(`  - name: "%s"
    type: hysteria2
    server: %s
    port: %d
    password: %s
    sni: %s
    alpn:
      - h3`, name, node.Host, ib.Port, user.UUID, domain)
}

func clashReality(user model.User, node model.Node, ib model.NodeInbound, s map[string]any, name string) string {
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

func clashHTTPUpgrade(user model.User, node model.Node, ib model.NodeInbound, s map[string]any, name string) string {
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
	return fmt.Sprintf(`  - name: "%s"
    type: vless
    server: %s
    port: 443
    udp: true
    uuid: %s
    tls: true
    servername: %s
    client-fingerprint: chrome
    network: httpupgrade
    httpupgrade-opts:
      path: %s
      host: %s`, name, domain, user.UUID, domain, path, domain)
}

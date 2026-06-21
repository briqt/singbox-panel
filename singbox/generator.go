package singbox

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/briqt/singbox-panel/model"
)

func hostForURI(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

func GenerateConfig(users []model.User, inbounds []model.NodeInbound) ([]byte, error) {
	ibList := buildInbounds(users, inbounds)

	config := map[string]any{
		"log": map[string]any{
			"level":     "info",
			"timestamp": true,
		},
		"experimental": map[string]any{
			"clash_api": map[string]any{
				"external_controller": "127.0.0.1:9090",
			},
		},
		"inbounds":  ibList,
		"outbounds": []map[string]any{{"type": "direct", "tag": "direct"}},
	}
	return json.MarshalIndent(config, "", "  ")
}

func buildInbounds(users []model.User, inbounds []model.NodeInbound) []map[string]any {
	var result []map[string]any
	usedTags := map[string]bool{}
	for _, ib := range inbounds {
		if !ib.Enabled {
			continue
		}
		var settings map[string]any
		json.Unmarshal(ib.Settings, &settings)
		if settings == nil {
			settings = map[string]any{}
		}
		tag := uniqueTag(ib.Tag, ib.Protocol, ib.Port, usedTags)
		switch ib.Protocol {
		case "hysteria2":
			result = append(result, buildHysteria2Inbound(users, ib, settings, tag))
		case "vless-reality":
			result = append(result, buildRealityInbound(users, ib, settings, tag))
		case "vless-httpupgrade":
			result = append(result, buildHTTPUpgradeInbound(users, ib, settings, tag))
		}
	}
	return result
}

func uniqueTag(tag, protocol string, port int, used map[string]bool) string {
	base := tag
	if base == "" {
		base = protocol
	}
	if !used[base] {
		used[base] = true
		return base
	}
	unique := fmt.Sprintf("%s-%d", base, port)
	used[unique] = true
	return unique
}

func buildHysteria2Inbound(users []model.User, ib model.NodeInbound, s map[string]any, tag string) map[string]any {
	hy2Users := make([]map[string]any, 0, len(users))
	for _, u := range users {
		hy2Users = append(hy2Users, map[string]any{"name": u.Name, "password": u.UUID})
	}
	domain, _ := s["domain"].(string)
	certPath, _ := s["cert_path"].(string)
	keyPath, _ := s["key_path"].(string)
	return map[string]any{
		"type": "hysteria2", "tag": tag,
		"listen": "::", "listen_port": ib.Port,
		"up_mbps": 1000, "down_mbps": 1000,
		"users": hy2Users,
		"tls": map[string]any{
			"enabled": true, "server_name": domain, "alpn": []string{"h3"},
			"certificate_path": certPath, "key_path": keyPath,
		},
	}
}

func buildRealityInbound(users []model.User, ib model.NodeInbound, s map[string]any, tag string) map[string]any {
	vlessUsers := make([]map[string]any, 0, len(users))
	for _, u := range users {
		vlessUsers = append(vlessUsers, map[string]any{"name": u.Name, "uuid": u.UUID, "flow": "xtls-rprx-vision"})
	}
	sni, _ := s["sni"].(string)
	privateKey, _ := s["private_key"].(string)
	shortID, _ := s["short_id"].(string)
	handshakeDest, _ := s["handshake_server"].(string)
	handshakePort := 443
	if hp, ok := s["handshake_port"].(float64); ok {
		handshakePort = int(hp)
	}
	if handshakeDest == "" {
		handshakeDest = sni
	}
	return map[string]any{
		"type": "vless", "tag": tag,
		"listen": "::", "listen_port": ib.Port,
		"users": vlessUsers,
		"tls": map[string]any{
			"enabled":     true,
			"server_name": sni,
			"reality": map[string]any{
				"enabled":     true,
				"private_key": privateKey,
				"short_id":    []string{shortID},
				"handshake": map[string]any{
					"server":      handshakeDest,
					"server_port": handshakePort,
				},
			},
		},
	}
}

func buildHTTPUpgradeInbound(users []model.User, ib model.NodeInbound, s map[string]any, tag string) map[string]any {
	vlessUsers := make([]map[string]any, 0, len(users))
	for _, u := range users {
		vlessUsers = append(vlessUsers, map[string]any{"name": u.Name, "uuid": u.UUID})
	}
	domain, _ := s["domain"].(string)
	certPath, _ := s["cert_path"].(string)
	keyPath, _ := s["key_path"].(string)
	path, _ := s["path"].(string)
	if path == "" {
		path = "/upgrade"
	}
	inbound := map[string]any{
		"type": "vless", "tag": tag,
		"listen": "::", "listen_port": ib.Port,
		"users": vlessUsers,
		"transport": map[string]any{
			"type": "httpupgrade",
			"host": domain,
			"path": path,
		},
	}
	if certPath != "" && keyPath != "" {
		inbound["tls"] = map[string]any{
			"enabled": true, "server_name": domain,
			"certificate_path": certPath, "key_path": keyPath,
		}
	}
	return inbound
}

// Subscription URI generation

func GenerateSubscription(user model.User, nodes []model.NodeWithInbounds) string {
	var lines []string
	for _, n := range nodes {
		if !n.Enabled {
			continue
		}
		names := protocolNameTracker{}
		for _, ib := range n.Inbounds {
			if !ib.Enabled {
				continue
			}
			uri := buildURI(user, n.Node, ib, names)
			if uri != "" {
				lines = append(lines, uri)
			}
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// protocolNameTracker deduplicates proxy names per node
type protocolNameTracker map[string]int

func (t protocolNameTracker) next(base string) string {
	t[base]++
	if t[base] == 1 {
		return base
	}
	return fmt.Sprintf("%s-%d", base, t[base])
}

func buildURI(user model.User, node model.Node, ib model.NodeInbound, names protocolNameTracker) string {
	var s map[string]any
	json.Unmarshal(ib.Settings, &s)
	if s == nil {
		s = map[string]any{}
	}
	switch ib.Protocol {
	case "hysteria2":
		return buildHysteria2URI(user, node, ib, s, names)
	case "vless-reality":
		return buildRealityURI(user, node, ib, s, names)
	case "vless-httpupgrade":
		return buildHTTPUpgradeURI(user, node, ib, s, names)
	}
	return ""
}

func buildHysteria2URI(user model.User, node model.Node, ib model.NodeInbound, s map[string]any, names protocolNameTracker) string {
	domain, _ := s["domain"].(string)
	if domain == "" {
		domain = node.Domain
	}
	if domain == "" {
		return ""
	}
	name := names.next(node.Name + "-Hy2")
	return fmt.Sprintf("hysteria2://%s@%s:%d?sni=%s&alpn=h3#%s",
		user.UUID, hostForURI(node.Host), ib.Port, domain, name)
}

func buildRealityURI(user model.User, node model.Node, ib model.NodeInbound, s map[string]any, names protocolNameTracker) string {
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
	name := names.next(node.Name + "-Reality")
	return fmt.Sprintf("vless://%s@%s:%d?encryption=none&security=reality&sni=%s&pbk=%s&sid=%s&fp=%s&flow=xtls-rprx-vision&type=tcp#%s",
		user.UUID, hostForURI(node.Host), ib.Port, sni, publicKey, shortID, fp, name)
}

func buildHTTPUpgradeURI(user model.User, node model.Node, ib model.NodeInbound, s map[string]any, names protocolNameTracker) string {
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
	// Client always connects to CF on 443 (CF handles TLS)
	name := names.next(node.Name + "-CDN")
	return fmt.Sprintf("vless://%s@%s:%d?encryption=none&security=tls&sni=%s&type=httpupgrade&host=%s&path=%s&fp=chrome#%s",
		user.UUID, domain, 443, domain, domain, path, name)
}

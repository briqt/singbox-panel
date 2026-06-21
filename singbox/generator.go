package singbox

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/briqt/singbox-panel/model"
)

func GenerateConfig(users []model.User, inbounds []model.NodeInbound) ([]byte, error) {
	config := map[string]any{
		"log": map[string]any{
			"level":     "info",
			"timestamp": true,
		},
		"inbounds":  buildInbounds(users, inbounds),
		"outbounds": []map[string]any{{"type": "direct", "tag": "direct"}},
	}
	return json.MarshalIndent(config, "", "  ")
}

func userNames(users []model.User) []string {
	names := make([]string, len(users))
	for i, u := range users {
		names[i] = u.Name
	}
	return names
}

func buildInbounds(users []model.User, inbounds []model.NodeInbound) []map[string]any {
	var result []map[string]any
	for _, ib := range inbounds {
		if !ib.Enabled {
			continue
		}
		var settings map[string]any
		json.Unmarshal(ib.Settings, &settings)
		if settings == nil {
			settings = map[string]any{}
		}
		switch ib.Protocol {
		case "hysteria2":
			result = append(result, buildHysteria2Inbound(users, ib, settings))
		case "vless-reality":
			result = append(result, buildRealityInbound(users, ib, settings))
		case "vless-httpupgrade":
			result = append(result, buildHTTPUpgradeInbound(users, ib, settings))
		}
	}
	return result
}

func buildHysteria2Inbound(users []model.User, ib model.NodeInbound, s map[string]any) map[string]any {
	hy2Users := make([]map[string]any, 0, len(users))
	for _, u := range users {
		hy2Users = append(hy2Users, map[string]any{"name": u.Name, "password": u.UUID})
	}
	domain, _ := s["domain"].(string)
	certPath, _ := s["cert_path"].(string)
	keyPath, _ := s["key_path"].(string)
	return map[string]any{
		"type": "hysteria2", "tag": tagOrDefault(ib.Tag, "hysteria2"),
		"listen": "::", "listen_port": ib.Port,
		"up_mbps": 1000, "down_mbps": 1000,
		"users": hy2Users,
		"tls": map[string]any{
			"enabled": true, "server_name": domain, "alpn": "h3",
			"certificate_path": certPath, "key_path": keyPath,
		},
	}
}

func buildRealityInbound(users []model.User, ib model.NodeInbound, s map[string]any) map[string]any {
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
		"type": "vless", "tag": tagOrDefault(ib.Tag, "vless-reality"),
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

func buildHTTPUpgradeInbound(users []model.User, ib model.NodeInbound, s map[string]any) map[string]any {
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
	return map[string]any{
		"type": "vless", "tag": tagOrDefault(ib.Tag, "vless-httpupgrade"),
		"listen": "::", "listen_port": ib.Port,
		"users": vlessUsers,
		"tls": map[string]any{
			"enabled": true, "server_name": domain,
			"certificate_path": certPath, "key_path": keyPath,
		},
		"transport": map[string]any{
			"type": "httpupgrade",
			"host": domain,
			"path": path,
		},
	}
}

func tagOrDefault(tag, def string) string {
	if tag != "" {
		return tag
	}
	return def
}

// Subscription URI generation

func GenerateSubscription(user model.User, nodes []model.NodeWithInbounds) string {
	var lines []string
	for _, n := range nodes {
		if !n.Enabled {
			continue
		}
		for _, ib := range n.Inbounds {
			if !ib.Enabled {
				continue
			}
			uri := buildURI(user, n.Node, ib)
			if uri != "" {
				lines = append(lines, uri)
			}
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func buildURI(user model.User, node model.Node, ib model.NodeInbound) string {
	var s map[string]any
	json.Unmarshal(ib.Settings, &s)
	if s == nil {
		s = map[string]any{}
	}
	switch ib.Protocol {
	case "hysteria2":
		return buildHysteria2URI(user, node, ib, s)
	case "vless-reality":
		return buildRealityURI(user, node, ib, s)
	case "vless-httpupgrade":
		return buildHTTPUpgradeURI(user, node, ib, s)
	}
	return ""
}

func buildHysteria2URI(user model.User, node model.Node, ib model.NodeInbound, s map[string]any) string {
	domain, _ := s["domain"].(string)
	if domain == "" {
		domain = node.Domain
	}
	if domain == "" {
		return ""
	}
	return fmt.Sprintf("hysteria2://%s@%s:%d?sni=%s&alpn=h3#%s-Hy2",
		user.UUID, node.Host, ib.Port, domain, node.Name)
}

func buildRealityURI(user model.User, node model.Node, ib model.NodeInbound, s map[string]any) string {
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
	return fmt.Sprintf("vless://%s@%s:%d?encryption=none&security=reality&sni=%s&pbk=%s&sid=%s&fp=%s&flow=xtls-rprx-vision&type=tcp#%s-Reality",
		user.UUID, node.Host, ib.Port, sni, publicKey, shortID, fp, node.Name)
}

func buildHTTPUpgradeURI(user model.User, node model.Node, ib model.NodeInbound, s map[string]any) string {
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
	host := node.Host
	if domain != "" {
		host = domain
	}
	return fmt.Sprintf("vless://%s@%s:%d?encryption=none&security=tls&sni=%s&type=httpupgrade&host=%s&path=%s&fp=chrome#%s-CDN",
		user.UUID, host, ib.Port, domain, domain, path, node.Name)
}

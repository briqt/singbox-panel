package singbox

import (
	"encoding/json"
	"fmt"

	"github.com/briqt/singbox-panel/model"
)

func GenerateConfig(users []model.User, inbounds []model.NodeInbound) ([]byte, error) {
	config := map[string]any{
		"log": map[string]any{
			"level":     "info",
			"timestamp": true,
		},
		"experimental": map[string]any{
			"v2ray_api": map[string]any{
				"listen": "127.0.0.1:15555",
				"stats": map[string]any{
					"enabled": true,
					"users":   userNames(users),
				},
			},
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
		case "vless-vision":
			result = append(result, buildVLESSVision(users, ib, settings))
		case "hysteria2":
			result = append(result, buildHysteria2(users, ib, settings))
		case "vless-reality":
			result = append(result, buildVLESSReality(users, ib, settings))
		}
	}
	return result
}

func buildVLESSVision(users []model.User, ib model.NodeInbound, settings map[string]any) map[string]any {
	vlessUsers := make([]map[string]any, 0, len(users))
	for _, u := range users {
		vlessUsers = append(vlessUsers, map[string]any{
			"name": u.Name,
			"uuid": u.UUID,
			"flow": "xtls-rprx-vision",
		})
	}
	inbound := map[string]any{
		"type":        "vless",
		"tag":         tagOrDefault(ib.Tag, "VLESSTCP"),
		"listen":      "::",
		"listen_port": ib.Port,
		"users":       vlessUsers,
	}
	if tls := buildTLSFromSettings(settings); tls != nil {
		inbound["tls"] = tls
	}
	return inbound
}

func buildHysteria2(users []model.User, ib model.NodeInbound, settings map[string]any) map[string]any {
	hy2Users := make([]map[string]any, 0, len(users))
	for _, u := range users {
		hy2Users = append(hy2Users, map[string]any{
			"name":     u.Name,
			"password": u.UUID,
		})
	}
	inbound := map[string]any{
		"type":        "hysteria2",
		"tag":         tagOrDefault(ib.Tag, "hysteria2"),
		"listen":      "::",
		"listen_port": ib.Port,
		"up_mbps":     1000,
		"down_mbps":   1000,
		"users":       hy2Users,
	}
	if tls := buildTLSFromSettings(settings); tls != nil {
		inbound["tls"] = tls
	}
	return inbound
}

func buildVLESSReality(users []model.User, ib model.NodeInbound, settings map[string]any) map[string]any {
	vlessUsers := make([]map[string]any, 0, len(users))
	for _, u := range users {
		vlessUsers = append(vlessUsers, map[string]any{
			"name": u.Name,
			"uuid": u.UUID,
			"flow": "xtls-rprx-vision",
		})
	}
	inbound := map[string]any{
		"type":        "vless",
		"tag":         tagOrDefault(ib.Tag, "VLESSReality"),
		"listen":      "::",
		"listen_port": ib.Port,
		"users":       vlessUsers,
	}
	if reality := buildRealityFromSettings(settings); reality != nil {
		inbound["tls"] = reality
	}
	return inbound
}

func buildTLSFromSettings(settings map[string]any) map[string]any {
	domain, _ := settings["tls_domain"].(string)
	certPath, _ := settings["cert_path"].(string)
	keyPath, _ := settings["key_path"].(string)
	if certPath == "" && domain == "" {
		return nil
	}
	tls := map[string]any{
		"enabled": true,
	}
	if domain != "" {
		tls["server_name"] = domain
	}
	if certPath != "" {
		tls["certificate_path"] = certPath
		tls["key_path"] = keyPath
	}
	if alpn, ok := settings["alpn"].(string); ok && alpn != "" {
		tls["alpn"] = alpn
	}
	return tls
}

func buildRealityFromSettings(settings map[string]any) map[string]any {
	sni, _ := settings["reality_sni"].(string)
	privateKey, _ := settings["reality_private_key"].(string)
	shortID, _ := settings["reality_short_id"].(string)
	if sni == "" || privateKey == "" {
		return nil
	}
	handshakeDest, _ := settings["reality_handshake_dest"].(string)
	handshakePort := 443
	if hp, ok := settings["reality_handshake_port"].(float64); ok {
		handshakePort = int(hp)
	}
	if handshakeDest == "" {
		handshakeDest = sni
	}
	reality := map[string]any{
		"enabled": true,
		"reality": map[string]any{
			"enabled":     true,
			"private_key": privateKey,
			"short_id":    []string{shortID},
			"handshake": map[string]any{
				"type":        "tcp",
				"server":      handshakeDest,
				"server_port": handshakePort,
			},
		},
		"server_name": sni,
	}
	return reality
}

func tagOrDefault(tag, def string) string {
	if tag != "" {
		return tag
	}
	return def
}

func GenerateSubscription(user model.User, nodes []model.NodeWithInbounds) string {
	var lines string
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
				lines += uri + "\n"
			}
		}
	}
	return lines
}

func buildURI(user model.User, node model.Node, ib model.NodeInbound) string {
	var settings map[string]any
	json.Unmarshal(ib.Settings, &settings)
	if settings == nil {
		settings = map[string]any{}
	}

	switch ib.Protocol {
	case "vless-vision":
		return buildVLESSVisionURI(user, node, ib, settings)
	case "hysteria2":
		return buildHysteria2URI(user, node, ib, settings)
	case "vless-reality":
		return buildVLESSRealityURI(user, node, ib, settings)
	}
	return ""
}

func buildVLESSVisionURI(user model.User, node model.Node, ib model.NodeInbound, settings map[string]any) string {
	domain, _ := settings["tls_domain"].(string)
	if domain == "" {
		domain = node.Host
	}
	return fmt.Sprintf("vless://%s@%s:%d?encryption=none&security=tls&sni=%s&flow=xtls-rprx-vision&type=tcp#%s-vless-vision",
		user.UUID, node.Host, ib.Port, domain, node.Name)
}

func buildHysteria2URI(user model.User, node model.Node, ib model.NodeInbound, settings map[string]any) string {
	domain, _ := settings["tls_domain"].(string)
	if domain == "" {
		domain = node.Host
	}
	return fmt.Sprintf("hysteria2://%s@%s:%d?sni=%s#%s-hysteria2",
		user.UUID, node.Host, ib.Port, domain, node.Name)
}

func buildVLESSRealityURI(user model.User, node model.Node, ib model.NodeInbound, settings map[string]any) string {
	sni, _ := settings["reality_sni"].(string)
	publicKey, _ := settings["reality_public_key"].(string)
	shortID, _ := settings["reality_short_id"].(string)
	if sni == "" || publicKey == "" {
		return ""
	}
	return fmt.Sprintf("vless://%s@%s:%d?encryption=none&security=reality&sni=%s&pbk=%s&sid=%s&flow=xtls-rprx-vision&type=tcp#%s-reality",
		user.UUID, node.Host, ib.Port, sni, publicKey, shortID, node.Name)
}

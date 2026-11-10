package singbox

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/briqt/singbox-panel/model"
)

func hostForURI(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

// V2RayAPIListen is the address sing-box exposes the v2ray_api StatsService on.
// The panel's traffic poller dials this (tunnelled over SSH) to read exact
// per-user uplink/downlink counters. Requires a sing-box binary built with the
// with_v2ray_api tag.
const V2RayAPIListen = "127.0.0.1:10085"

func GenerateConfig(users []model.User, inbounds []model.NodeInbound) ([]byte, error) {
	return generateConfig(func(model.NodeInbound) []model.User { return users }, inbounds)
}

// GenerateConfigForInbounds 是生产配置入口。无授权用户的入站不监听，避免协议
// 实现把空认证列表解释成免认证；新增线路因此在显式授权前保持关闭。
func GenerateConfigForInbounds(usersByInbound map[int][]model.User, inbounds []model.NodeInbound) ([]byte, error) {
	active := make([]model.NodeInbound, 0, len(inbounds))
	for _, ib := range inbounds {
		if len(usersByInbound[ib.ID]) > 0 {
			active = append(active, ib)
		}
	}
	return generateConfig(func(ib model.NodeInbound) []model.User { return usersByInbound[ib.ID] }, active)
}

func generateConfig(usersFor func(model.NodeInbound) []model.User, inbounds []model.NodeInbound) ([]byte, error) {
	ibList := make([]map[string]any, 0, len(inbounds))
	outbounds := []map[string]any{{"type": "direct", "tag": "direct"}}
	var rules []map[string]any
	var names []string
	seenUsers := map[string]bool{}
	usedTags := map[string]bool{}
	for _, ib := range inbounds {
		if !ib.Enabled {
			continue
		}
		if err := model.ValidateOutbound(ib.Outbound); err != nil {
			return nil, fmt.Errorf("inbound %d: %w", ib.ID, err)
		}
		var settings map[string]any
		if len(ib.Settings) > 0 {
			if err := json.Unmarshal(ib.Settings, &settings); err != nil {
				return nil, fmt.Errorf("inbound %d: invalid settings", ib.ID)
			}
		}
		if settings == nil {
			settings = map[string]any{}
		}
		tag := uniqueTag(ib.Tag, ib.Protocol, ib.Port, usedTags)
		users := usersFor(ib)
		var inbound map[string]any
		switch ib.Protocol {
		case "hysteria2":
			inbound = buildHysteria2Inbound(users, ib, settings, tag)
		case "vless-reality":
			inbound = buildRealityInbound(users, ib, settings, tag)
		case "vless-httpupgrade":
			inbound = buildHTTPUpgradeInbound(users, ib, settings, tag)
		default:
			return nil, fmt.Errorf("inbound %d: unsupported protocol", ib.ID)
		}
		ibList = append(ibList, inbound)
		for _, user := range users {
			if !seenUsers[user.Name] {
				seenUsers[user.Name] = true
				names = append(names, user.Name)
			}
		}
		if upstream := ib.Outbound; upstream != nil {
			// 直接引用本轮实际生成的 tag，重排和去重不会导致路由串线。
			outboundTag := "upstream-" + tag
			outbound := map[string]any{
				"type": "socks", "tag": outboundTag, "version": "5",
				"server": upstream.Server, "server_port": upstream.ServerPort,
				"network": "tcp",
			}
			if upstream.Username != "" {
				outbound["username"] = upstream.Username
				outbound["password"] = upstream.Password
			}
			outbounds = append(outbounds, outbound)
			rules = append(rules, map[string]any{"inbound": []string{tag}, "action": "route", "outbound": outboundTag})
		}
	}
	experimental := map[string]any{"clash_api": map[string]any{"external_controller": "127.0.0.1:9090"}}
	if len(ibList) > 0 && len(names) > 0 {
		experimental["v2ray_api"] = map[string]any{
			"listen": V2RayAPIListen,
			"stats":  map[string]any{"enabled": true, "inbounds": inboundTags(ibList), "users": names},
		}
	}
	config := map[string]any{
		"log":          map[string]any{"level": "info", "timestamp": true},
		"experimental": experimental, "inbounds": ibList, "outbounds": outbounds,
	}
	// 旧配置不凭空增加 route，保证未使用上游的节点生成结果保持兼容。
	if len(rules) > 0 {
		config["route"] = map[string]any{"rules": rules, "final": "direct"}
	}
	return json.MarshalIndent(config, "", "  ")
}

func inboundTags(ibList []map[string]any) []string {
	tags := make([]string, 0, len(ibList))
	for _, ib := range ibList {
		if tag, ok := ib["tag"].(string); ok && tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

func uniqueTag(tag, protocol string, port int, used map[string]bool) string {
	base := tag
	if base == "" {
		base = protocol
	}
	candidate := base
	for suffix := 0; used[candidate]; suffix++ {
		candidate = fmt.Sprintf("%s-%d", base, port)
		if suffix > 0 {
			candidate = fmt.Sprintf("%s-%d-%d", base, port, suffix)
		}
	}
	used[candidate] = true
	return candidate
}

func buildHysteria2Inbound(users []model.User, ib model.NodeInbound, s map[string]any, tag string) map[string]any {
	hy2Users := make([]map[string]any, 0, len(users))
	for _, u := range users {
		hy2Users = append(hy2Users, map[string]any{"name": u.Name, "password": u.UUID})
	}
	domain, _ := s["domain"].(string)
	certPath, _ := s["cert_path"].(string)
	keyPath, _ := s["key_path"].(string)
	inbound := map[string]any{
		"type": "hysteria2", "tag": tag,
		"listen": "::", "listen_port": ib.Port,
		"users": hy2Users,
		// Without bandwidth limits this tells clients to use BBR instead of
		// Hysteria's Brutal CC. Brutal sends at the client's declared rate and
		// treats loss as noise, which is the wrong reflex on a link that is
		// being shaped rather than congested: carrier UDP QoS shows up as loss,
		// Brutal answers by sending harder, and the result is the stall-recover
		// cycle. BBR backs off and the tunnel stays usable. The cost is peak
		// throughput on a clean link — drop this key to get Brutal back.
		"ignore_client_bandwidth": true,
		"tls": map[string]any{
			"enabled": true, "server_name": domain, "alpn": []string{"h3"},
			"certificate_path": certPath, "key_path": keyPath,
		},
	}
	// Salamander wraps QUIC into what looks like random UDP. A bare QUIC
	// handshake on a fixed port is the pattern carrier UDP QoS matches on.
	// Enabling it means the port is no longer a valid HTTP/3 endpoint, which is
	// why there is no masquerade fallback to pair with it.
	if obfsPassword := hysteriaObfsPassword(s); obfsPassword != "" {
		inbound["obfs"] = map[string]any{
			"type":     "salamander",
			"password": obfsPassword,
		}
	} else {
		// Served by sing-box itself: no web server, process or runtime dependency.
		inbound["masquerade"] = builtinMasquerade()
	}
	return inbound
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
	name := clashName(node, ib, names)
	obfs := ""
	if obfsPassword := hysteriaObfsPassword(s); obfsPassword != "" {
		obfs = "&obfs=salamander&obfs-password=" + url.QueryEscape(obfsPassword)
	}
	return fmt.Sprintf("hysteria2://%s@%s:%d?sni=%s&alpn=h3%s#%s",
		user.UUID, hostForURI(domain), ib.Port, domain, obfs, url.PathEscape(name))
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
	name := clashName(node, ib, names)
	return fmt.Sprintf("vless://%s@%s:%d?encryption=none&security=reality&sni=%s&pbk=%s&sid=%s&fp=%s&flow=xtls-rprx-vision&type=tcp#%s",
		user.UUID, hostForURI(realityServer(node.Host, s)), ib.Port, sni, publicKey, shortID, fp, url.PathEscape(name))
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
	name := clashName(node, ib, names)
	return fmt.Sprintf("vless://%s@%s:%d?encryption=none&security=tls&sni=%s&type=httpupgrade&host=%s&path=%s&fp=chrome#%s",
		user.UUID, domain, 443, domain, domain, path, name)
}

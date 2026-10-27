package singbox

import _ "embed"

//go:embed site.html
var siteHTML string

// HysteriaProfile preserves an existing deployment unless a profile is chosen.
// QUIC keeps HTTP/3 available; Salamander requires matching client credentials.
func HysteriaProfile(settings map[string]any) string {
	if profile, _ := settings["profile"].(string); profile != "" {
		return profile
	}
	if password, _ := settings["obfs_password"].(string); password != "" {
		return "salamander"
	}
	return "quic"
}

func hysteriaObfsPassword(settings map[string]any) string {
	if HysteriaProfile(settings) != "salamander" {
		return ""
	}
	password, _ := settings["obfs_password"].(string)
	return password
}

func builtinMasquerade() map[string]any {
	return map[string]any{
		"type": "string", "status_code": 200,
		"headers": map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
		"content": siteHTML,
	}
}

// Reality's server address is independent of its camouflage SNI. In CDN mode
// node.Domain points to a CDN which cannot carry Reality; only auto-setup after
// a successful direct-DNS assessment may store it as the connection address.
func realityServer(nodeHost string, settings map[string]any) string {
	if server, _ := settings["server"].(string); server != "" {
		return server
	}
	return nodeHost
}

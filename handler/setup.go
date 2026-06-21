package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strings"

	"github.com/briqt/singbox-panel/model"
)

type SetupHandler struct {
	Nodes  *model.NodeStore
	Config *ConfigHandler
	Ops    *NodeOpsHandler
}

type AutoSetupReq struct {
	Domain    string   `json:"domain"`
	Protocols []string `json:"protocols"`
	Ports     struct {
		Hysteria2   int `json:"hysteria2"`
		Reality     int `json:"reality"`
		HTTPUpgrade int `json:"httpupgrade"`
	} `json:"ports"`
}

var realitySNIs = []string{
	"www.apple.com",
	"www.microsoft.com",
	"www.amazon.com",
	"www.cloudflare.com",
	"www.mozilla.org",
	"www.samsung.com",
	"www.intel.com",
	"www.nvidia.com",
	"swcdn.apple.com",
	"updates.cdn-apple.com",
}

func (h *SetupHandler) HandleAutoSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}

	nodeID := parseNodeIDFromConfigPath(r.URL.Path)
	node, err := h.Nodes.Get(nodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}

	var req AutoSetupReq
	json.NewDecoder(r.Body).Decode(&req)

	domain := req.Domain
	if domain == "" {
		domain = node.Domain
	}

	// Default protocols based on domain + DNS conditions
	if len(req.Protocols) == 0 {
		if domain != "" {
			// Check if domain resolves to node's real IP (DNS-only) or CF proxy
			if isDNSDirect(domain, node.Host) {
				req.Protocols = []string{"hysteria2", "vless-reality"}
			} else {
				// CF proxied or DNS mismatch → use HTTPUpgrade (CDN) + Reality (IP direct)
				req.Protocols = []string{"vless-httpupgrade", "vless-reality"}
			}
		} else {
			req.Protocols = []string{"vless-reality"}
		}
	}

	// Update node domain if provided
	if req.Domain != "" && req.Domain != node.Domain {
		h.Nodes.DB.Exec(`UPDATE nodes SET domain = ? WHERE id = ?`, req.Domain, node.ID)
	}

	// Connect to node
	client, err := h.Config.sshConnect(node)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ssh connect failed: "+err.Error())
		return
	}
	defer client.Close()

	type inboundResult struct {
		Protocol string `json:"protocol"`
		Port     int    `json:"port"`
		Status   string `json:"status"`
		Details  any    `json:"details,omitempty"`
	}
	var results []inboundResult

	// Check existing inbounds to avoid duplicates
	existingInbounds, _ := h.Nodes.ListInbounds(node.ID)
	existingProtos := map[string]bool{}
	for _, ib := range existingInbounds {
		existingProtos[ib.Protocol] = true
	}

	for _, proto := range req.Protocols {
		if existingProtos[proto] {
			results = append(results, inboundResult{Protocol: proto, Status: "skipped", Details: "already configured"})
			continue
		}
		switch proto {
		case "hysteria2":
			if domain == "" {
				results = append(results, inboundResult{Protocol: proto, Status: "skipped", Details: "no domain available"})
				continue
			}
			ips, dnsErr := net.LookupHost(domain)
			if dnsErr != nil {
				results = append(results, inboundResult{Protocol: proto, Status: "error", Details: "DNS lookup failed: " + dnsErr.Error()})
				continue
			}
			dnsOK := false
			for _, ip := range ips {
				if ip == node.Host {
					dnsOK = true
				}
			}
			if !dnsOK {
				results = append(results, inboundResult{Protocol: proto, Status: "error", Details: fmt.Sprintf("DNS: %s → %v, expected %s", domain, ips, node.Host)})
				continue
			}
			port := req.Ports.Hysteria2
			if port == 0 {
				port = randomPort()
			}
			certPath := fmt.Sprintf("/etc/sing-box/tls/%s.crt", domain)
			keyPath := fmt.Sprintf("/etc/sing-box/tls/%s.key", domain)
			certScript := fmt.Sprintf(`
mkdir -p /etc/sing-box/tls
if [ -f %s ] && [ -f %s ]; then echo "CERT_EXISTS"; exit 0; fi
if ! command -v /root/.acme.sh/acme.sh &>/dev/null; then curl -sL https://get.acme.sh | sh -s email=acme@%s 2>&1; fi
/root/.acme.sh/acme.sh --set-default-ca --server letsencrypt 2>/dev/null
# Determine ACME challenge mode
ACME_MODE="--standalone"
if command -v caddy &>/dev/null && systemctl is-active caddy &>/dev/null; then
  # Use Caddy's file_server via a temp webroot
  WEBROOT="/var/www/acme"
  mkdir -p "$WEBROOT"
  ACME_MODE="--webroot $WEBROOT"
  # Ensure Caddy serves the ACME challenge path
  if ! grep -q "%s" /etc/caddy/Caddyfile 2>/dev/null; then
    printf '\nhttp://%s {\n  root * /var/www/acme\n  file_server\n}\n' "%s" >> /etc/caddy/Caddyfile
    systemctl reload caddy 2>/dev/null; sleep 1
  fi
elif ss -tlnp | grep -q ':80 '; then
  # Something else on port 80 - stop it temporarily
  PORT80_SVC=$(ss -tlnp | grep ':80 ' | grep -oP 'users:\(\("\K[^"]+' || true)
  if [ -n "$PORT80_SVC" ]; then systemctl stop "$PORT80_SVC" 2>/dev/null || true; sleep 1; fi
fi
/root/.acme.sh/acme.sh --issue -d %s $ACME_MODE --keylength ec-256 --force 2>&1 || true
# Restart stopped service if standalone was used
if [ -n "${PORT80_SVC:-}" ]; then systemctl start "$PORT80_SVC" 2>/dev/null || true; fi
/root/.acme.sh/acme.sh --install-cert -d %s --ecc --fullchain-file %s --key-file %s --reloadcmd "systemctl restart sing-box 2>/dev/null || true" 2>&1
/root/.acme.sh/acme.sh --install-cronjob 2>/dev/null
test -f %s && test -f %s && echo "CERT_OK"
`, certPath, keyPath, domain, domain, domain, domain, domain, domain, certPath, keyPath, certPath, keyPath)
			certOut, _ := sshRun(client, certScript)
			if certOut == "" || (!contains(certOut, "CERT_OK") && !contains(certOut, "CERT_EXISTS")) {
				results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: "cert install failed"})
				continue
			}
			settings := mustMarshal(map[string]any{"domain": domain, "cert_path": certPath, "key_path": keyPath})
			h.Nodes.CreateInbound(node.ID, model.CreateInboundReq{Tag: "hysteria2", Protocol: "hysteria2", Port: port, Settings: settings})
			results = append(results, inboundResult{Protocol: proto, Port: port, Status: "ok"})

		case "vless-reality":
			port := req.Ports.Reality
			if port == 0 {
				port = randomPort()
			}
			keypairOut, err := sshRun(client, node.SingboxBin+" generate reality-keypair")
			if err != nil {
				results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: "keypair generation failed"})
				continue
			}
			privateKey, publicKey := parseKeypair(keypairOut)
			shortIDOut, _ := sshRun(client, node.SingboxBin+" generate rand 8 --hex")
			shortID := trimOutput(shortIDOut)
			if shortID == "" {
				shortID = randomHex(8)
			}
			// Pick a random mainstream SNI for disguise
			sni := realitySNIs[randomPort()%len(realitySNIs)]
			settings := mustMarshal(map[string]any{
				"sni": sni, "private_key": privateKey, "public_key": publicKey,
				"short_id": shortID, "handshake_server": sni, "handshake_port": 443,
				"fingerprint": "chrome",
			})
			h.Nodes.CreateInbound(node.ID, model.CreateInboundReq{Tag: "vless-reality", Protocol: "vless-reality", Port: port, Settings: settings})
			results = append(results, inboundResult{Protocol: proto, Port: port, Status: "ok", Details: map[string]string{"public_key": publicKey, "short_id": shortID}})

		case "vless-httpupgrade":
			if domain == "" {
				results = append(results, inboundResult{Protocol: proto, Status: "skipped", Details: "no domain available"})
				continue
			}
			port := req.Ports.HTTPUpgrade
			if port == 0 {
				port = 443
			}
			path := "/" + randomHex(8)

			// HTTPUpgrade behind CF requires Origin Certificate (cert_path + key_path)
			// Check if cert files already exist on node, or if provided in request
			certPath := fmt.Sprintf("/etc/sing-box/tls/%s.crt", domain)
			keyPath := fmt.Sprintf("/etc/sing-box/tls/%s.key", domain)
			certCheck, _ := sshRun(client, fmt.Sprintf("test -f %s && test -f %s && echo OK", certPath, keyPath))
			if !contains(certCheck, "OK") {
				results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: "CF Origin Certificate required: upload cert/key via node settings first"})
				continue
			}

			settings := map[string]any{"domain": domain, "path": path, "cert_path": certPath, "key_path": keyPath}
			h.Nodes.CreateInbound(node.ID, model.CreateInboundReq{Tag: "vless-httpupgrade", Protocol: "vless-httpupgrade", Port: port, Settings: mustMarshal(settings)})
			results = append(results, inboundResult{Protocol: proto, Port: port, Status: "ok", Details: map[string]string{"path": path}})
		}
	}

	// Push config
	configBytes, err := h.Config.generateConfig(node.ID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"inbounds": results, "push": "error: " + err.Error()})
		return
	}
	if err := h.Config.pushViaSSH(node, configBytes); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"inbounds": results, "push": "error: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"inbounds": results, "push": "ok", "node": node.Name})
}

func randomPort() int {
	n, _ := rand.Int(rand.Reader, big.NewInt(30000))
	return int(n.Int64()) + 20000
}

func randomHex(bytes int) string {
	b := make([]byte, bytes)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func parseKeypair(output string) (privateKey, publicKey string) {
	for _, line := range splitLines(output) {
		if contains(line, "PrivateKey:") {
			privateKey = trimOutput(line[len("PrivateKey:"):])
		}
		if contains(line, "PublicKey:") {
			publicKey = trimOutput(line[len("PublicKey:"):])
		}
	}
	return
}

func splitLines(s string) []string {
	var result []string
	for _, line := range strings.Split(s, "\n") {
		result = append(result, strings.TrimSpace(line))
	}
	return result
}

func trimOutput(s string) string {
	return strings.TrimSpace(s)
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func isDNSDirect(domain, nodeHost string) bool {
	ips, err := net.LookupHost(domain)
	if err != nil {
		return false
	}
	for _, ip := range ips {
		if ip == nodeHost {
			return true
		}
	}
	return false
}

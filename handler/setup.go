package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
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

	// Default protocols based on conditions
	if len(req.Protocols) == 0 {
		if req.Domain != "" || node.Domain != "" {
			req.Protocols = []string{"hysteria2", "vless-reality"}
		} else {
			req.Protocols = []string{"vless-reality"}
		}
	}

	domain := req.Domain
	if domain == "" {
		domain = node.Domain
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

	for _, proto := range req.Protocols {
		switch proto {
		case "hysteria2":
			if domain == "" {
				results = append(results, inboundResult{Protocol: proto, Status: "skipped", Details: "no domain available"})
				continue
			}
			port := req.Ports.Hysteria2
			if port == 0 {
				port = randomPort()
			}
			// Issue cert
			certPath := fmt.Sprintf("/etc/sing-box/tls/%s.crt", domain)
			keyPath := fmt.Sprintf("/etc/sing-box/tls/%s.key", domain)
			certScript := fmt.Sprintf(`
mkdir -p /etc/sing-box/tls
if [ -f %s ] && [ -f %s ]; then echo "CERT_EXISTS"; exit 0; fi
if ! command -v /root/.acme.sh/acme.sh &>/dev/null; then curl -sL https://get.acme.sh | sh -s email=admin@%s 2>&1; fi
/root/.acme.sh/acme.sh --issue -d %s --standalone --keylength ec-256 --force 2>&1 || true
/root/.acme.sh/acme.sh --install-cert -d %s --ecc --fullchain-file %s --key-file %s --reloadcmd "systemctl restart sing-box 2>/dev/null || true" 2>&1
test -f %s && test -f %s && echo "CERT_OK"
`, certPath, keyPath, domain, domain, domain, certPath, keyPath, certPath, keyPath)
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
			// Generate keypair on node
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
			settings := mustMarshal(map[string]any{
				"sni": "www.microsoft.com", "private_key": privateKey, "public_key": publicKey,
				"short_id": shortID, "handshake_server": "www.microsoft.com", "handshake_port": 443,
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
			certPath := fmt.Sprintf("/etc/sing-box/tls/%s.crt", domain)
			keyPath := fmt.Sprintf("/etc/sing-box/tls/%s.key", domain)
			settings := mustMarshal(map[string]any{"domain": domain, "cert_path": certPath, "key_path": keyPath, "path": path})
			h.Nodes.CreateInbound(node.ID, model.CreateInboundReq{Tag: "vless-httpupgrade", Protocol: "vless-httpupgrade", Port: port, Settings: settings})
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
	n, _ := rand.Int(rand.Reader, big.NewInt(20000))
	return int(n.Int64()) + 10000
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
	var lines []string
	for _, l := range [2]string{"\n", "\r\n"} {
		_ = l
	}
	return append(lines, split(s)...)
}

func split(s string) []string {
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

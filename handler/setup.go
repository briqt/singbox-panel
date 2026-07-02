package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strconv"
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
	Mode      string   `json:"mode"`
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
	if node.ProxyType != "singbox" {
		writeError(w, http.StatusBadRequest, "auto-setup is only supported for singbox nodes")
		return
	}

	var req AutoSetupReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	domain := req.Domain
	if domain == "" {
		domain = node.Domain
	}
	if domain != "" && !validDomainName(domain) {
		writeError(w, http.StatusBadRequest, "invalid domain")
		return
	}

	protocols, assessment, err := suggestedProtocolsForRequest(req, node)
	if err != nil {
		status := http.StatusBadRequest
		if assessment != nil {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, map[string]any{"error": err.Error(), "assessment": assessment})
		return
	}
	req.Protocols = protocols
	seenProtocols := make(map[string]bool, len(req.Protocols))
	for _, protocol := range req.Protocols {
		if seenProtocols[protocol] {
			writeError(w, http.StatusBadRequest, "duplicate protocol: "+protocol)
			return
		}
		seenProtocols[protocol] = true
		switch protocol {
		case "hysteria2", "vless-httpupgrade":
			if domain == "" {
				writeError(w, http.StatusBadRequest, protocol+" requires a domain")
				return
			}
		case "vless-reality":
		default:
			writeError(w, http.StatusBadRequest, "unsupported protocol: "+protocol)
			return
		}
	}
	for protocol, port := range map[string]int{
		"hysteria2": req.Ports.Hysteria2, "vless-reality": req.Ports.Reality, "vless-httpupgrade": req.Ports.HTTPUpgrade,
	} {
		if port < 0 || port > 65535 {
			writeError(w, http.StatusBadRequest, "invalid port for "+protocol)
			return
		}
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

	// Existing protocols are updated in place when their domain or requested
	// port changes. Reality credentials remain stable unless the inbound is
	// explicitly deleted first.
	existingInbounds, err := h.Nodes.ListInbounds(node.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	existingProtos := map[string]model.NodeInbound{}
	for _, ib := range existingInbounds {
		existingProtos[ib.Protocol] = ib
	}
	if domain != node.Domain {
		selected := make(map[string]bool, len(req.Protocols))
		for _, protocol := range req.Protocols {
			selected[protocol] = true
		}
		for _, inbound := range existingInbounds {
			if (inbound.Protocol == "hysteria2" || inbound.Protocol == "vless-httpupgrade") && !selected[inbound.Protocol] {
				writeError(w, http.StatusConflict, "domain migration must include every existing domain-bound protocol")
				return
			}
		}
	}

	var createdInboundIDs []int
	var updatedInbounds []model.NodeInbound
	rollbackDatabase := func() error {
		var rollbackErrors []error
		for _, inboundID := range createdInboundIDs {
			if err := h.Nodes.DeleteInbound(inboundID); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("remove inbound %d: %w", inboundID, err))
			}
		}
		for _, inbound := range updatedInbounds {
			if _, err := h.Nodes.UpdateInbound(inbound.ID, model.CreateInboundReq{
				Tag: inbound.Tag, Protocol: inbound.Protocol, Port: inbound.Port, Settings: inbound.Settings,
			}); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore inbound %d: %w", inbound.ID, err))
			}
		}
		if domain != node.Domain {
			oldDomain := node.Domain
			if _, err := h.Nodes.Update(node.ID, model.UpdateNodeReq{Domain: &oldDomain}); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore node domain: %w", err))
			}
		}
		return errors.Join(rollbackErrors...)
	}

	hadError := false
	for _, proto := range req.Protocols {
		existing, exists := existingProtos[proto]
		switch proto {
		case "hysteria2":
			if domain == "" {
				results = append(results, inboundResult{Protocol: proto, Status: "error", Details: "domain is required"})
				hadError = true
				continue
			}
			var oldSettings map[string]any
			if exists {
				json.Unmarshal(existing.Settings, &oldSettings)
				oldDomain, _ := oldSettings["domain"].(string)
				if oldDomain == domain && (req.Ports.Hysteria2 == 0 || req.Ports.Hysteria2 == existing.Port) {
					results = append(results, inboundResult{Protocol: proto, Port: existing.Port, Status: "skipped", Details: "already configured"})
					continue
				}
			}
			ips, dnsErr := net.LookupHost(domain)
			if dnsErr != nil {
				results = append(results, inboundResult{Protocol: proto, Status: "error", Details: "DNS lookup failed: " + dnsErr.Error()})
				hadError = true
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
				hadError = true
				continue
			}
			port := req.Ports.Hysteria2
			if port == 0 {
				if exists {
					port = existing.Port
				} else {
					port = randomPort()
				}
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
				hadError = true
				continue
			}
			settings := mustMarshal(map[string]any{"domain": domain, "cert_path": certPath, "key_path": keyPath})
			inboundReq := model.CreateInboundReq{Tag: "hysteria2", Protocol: "hysteria2", Port: port, Settings: settings}
			status := "ok"
			if exists {
				updatedInbounds = append(updatedInbounds, existing)
				if _, err := h.Nodes.UpdateInbound(existing.ID, inboundReq); err != nil {
					results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: err.Error()})
					hadError = true
					continue
				}
				status = "updated"
			} else {
				inbound, err := h.Nodes.CreateInbound(node.ID, inboundReq)
				if err != nil {
					results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: err.Error()})
					hadError = true
					continue
				}
				createdInboundIDs = append(createdInboundIDs, inbound.ID)
			}
			results = append(results, inboundResult{Protocol: proto, Port: port, Status: status})

		case "vless-reality":
			if exists {
				results = append(results, inboundResult{Protocol: proto, Port: existing.Port, Status: "skipped", Details: "already configured"})
				continue
			}
			port := req.Ports.Reality
			if port == 0 {
				port = randomPort()
			}
			keypairOut, err := sshRun(client, node.SingboxBin+" generate reality-keypair")
			if err != nil {
				results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: "keypair generation failed"})
				hadError = true
				continue
			}
			privateKey, publicKey := parseKeypair(keypairOut)
			if privateKey == "" || publicKey == "" {
				results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: "keypair output was invalid"})
				hadError = true
				continue
			}
			shortIDOut, _ := sshRun(client, node.SingboxBin+" generate rand 8 --hex")
			shortID := trimOutput(shortIDOut)
			if shortID == "" {
				shortID = randomHex(8)
			}
			sni, probeErr := selectRealitySNI(func(command string) (string, error) {
				return sshRun(client, command)
			})
			if probeErr != nil {
				results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: probeErr.Error()})
				hadError = true
				continue
			}
			settings := mustMarshal(map[string]any{
				"sni": sni, "private_key": privateKey, "public_key": publicKey,
				"short_id": shortID, "handshake_server": sni, "handshake_port": 443,
				"fingerprint": "chrome",
			})
			inbound, err := h.Nodes.CreateInbound(node.ID, model.CreateInboundReq{Tag: "vless-reality", Protocol: "vless-reality", Port: port, Settings: settings})
			if err != nil {
				results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: err.Error()})
				hadError = true
				continue
			}
			createdInboundIDs = append(createdInboundIDs, inbound.ID)
			results = append(results, inboundResult{Protocol: proto, Port: port, Status: "ok", Details: map[string]string{
				"public_key": publicKey, "short_id": shortID, "sni": sni,
			}})

		case "vless-httpupgrade":
			if domain == "" {
				results = append(results, inboundResult{Protocol: proto, Status: "error", Details: "domain is required"})
				hadError = true
				continue
			}
			var oldSettings map[string]any
			if exists {
				json.Unmarshal(existing.Settings, &oldSettings)
				oldDomain, _ := oldSettings["domain"].(string)
				if oldDomain == domain && (req.Ports.HTTPUpgrade == 0 || req.Ports.HTTPUpgrade == existing.Port) {
					results = append(results, inboundResult{Protocol: proto, Port: existing.Port, Status: "skipped", Details: "already configured"})
					continue
				}
			}
			port := req.Ports.HTTPUpgrade
			if port == 0 {
				if exists {
					port = existing.Port
				} else {
					port = 443
				}
			}
			path := ""
			if exists {
				path, _ = oldSettings["path"].(string)
			}
			if path == "" {
				path = "/" + randomHex(8)
			}

			// HTTPUpgrade behind CF requires Origin Certificate (cert_path + key_path)
			// Check if cert files already exist on node, or if provided in request
			certPath := fmt.Sprintf("/etc/sing-box/tls/%s.crt", domain)
			keyPath := fmt.Sprintf("/etc/sing-box/tls/%s.key", domain)
			certCheck, _ := sshRun(client, fmt.Sprintf("test -f %s && test -f %s && echo OK", certPath, keyPath))
			if !contains(certCheck, "OK") {
				results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: "CF Origin Certificate required: upload cert/key via node settings first"})
				hadError = true
				continue
			}

			settings := map[string]any{"domain": domain, "path": path, "cert_path": certPath, "key_path": keyPath}
			inboundReq := model.CreateInboundReq{Tag: "vless-httpupgrade", Protocol: "vless-httpupgrade", Port: port, Settings: mustMarshal(settings)}
			status := "ok"
			if exists {
				updatedInbounds = append(updatedInbounds, existing)
				if _, err := h.Nodes.UpdateInbound(existing.ID, inboundReq); err != nil {
					results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: err.Error()})
					hadError = true
					continue
				}
				status = "updated"
			} else {
				inbound, err := h.Nodes.CreateInbound(node.ID, inboundReq)
				if err != nil {
					results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: err.Error()})
					hadError = true
					continue
				}
				createdInboundIDs = append(createdInboundIDs, inbound.ID)
			}
			results = append(results, inboundResult{Protocol: proto, Port: port, Status: status, Details: map[string]string{"path": path}})
		default:
			results = append(results, inboundResult{Protocol: proto, Status: "error", Details: "unsupported protocol"})
			hadError = true
		}
	}

	if hadError {
		if rollbackErr := rollbackDatabase(); rollbackErr != nil {
			writeError(w, http.StatusInternalServerError, "auto-setup failed and database rollback failed: "+rollbackErr.Error())
			return
		}
		var failureDetails []string
		for _, result := range results {
			if result.Status == "error" {
				failureDetails = append(failureDetails, result.Protocol+": "+fmt.Sprint(result.Details))
			}
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":    "auto-setup failed; no changes were applied: " + strings.Join(failureDetails, "; "),
			"inbounds": results,
			"push":     "not attempted",
		})
		return
	}

	if domain != node.Domain {
		if _, err := h.Nodes.Update(node.ID, model.UpdateNodeReq{Domain: &domain}); err != nil {
			if rollbackErr := rollbackDatabase(); rollbackErr != nil {
				writeError(w, http.StatusInternalServerError, "update node domain failed and rollback failed: "+rollbackErr.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "update node domain: "+err.Error())
			return
		}
	}

	// Generate and push under the node lock so concurrent panel operations
	// cannot publish a configuration snapshot created before a newer change.
	syncResults := h.Config.SyncNodes([]int{node.ID})
	if syncErr := syncFailure(syncResults); syncErr != nil {
		if rollbackErr := rollbackDatabase(); rollbackErr != nil {
			writeError(w, http.StatusInternalServerError, "push failed and database rollback failed: "+rollbackErr.Error())
			return
		}
		restoreResults := h.Config.SyncNodes([]int{node.ID})
		if restoreErr := syncFailure(restoreResults); restoreErr != nil {
			writeError(w, http.StatusBadGateway, "changes rolled back, but restoring the previous node config failed: "+restoreErr.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "push failed; changes rolled back: "+syncErr.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"inbounds": results, "push": "ok", "sync": syncResults, "node": node.Name, "assessment": assessment,
	})
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

type commandRunner func(command string) (string, error)

func selectRealitySNI(run commandRunner) (string, error) {
	var script strings.Builder
	for _, host := range realitySNIs {
		fmt.Fprintf(&script, "(metric=$(curl -sS -o /dev/null --connect-timeout 3 --max-time 5 --tlsv1.3 --tls-max 1.3 -w '%%{time_appconnect}' 'https://%s/' 2>/dev/null) && printf '%s %%s\\n' \"$metric\") &\n", host, host)
	}
	script.WriteString("wait\n")
	out, err := run(script.String())
	if err != nil && strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("unable to probe Reality handshake targets")
	}
	host, _ := parseFastestRealityProbe(out)
	if host == "" {
		return "", fmt.Errorf("no Reality handshake target completed a TLS 1.3 probe")
	}
	return host, nil
}

func parseFastestRealityProbe(output string) (string, float64) {
	allowed := make(map[string]bool, len(realitySNIs))
	for _, host := range realitySNIs {
		allowed[host] = true
	}
	bestHost := ""
	bestLatency := 0.0
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !allowed[fields[0]] {
			continue
		}
		latency, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || latency <= 0 {
			continue
		}
		if bestHost == "" || latency < bestLatency {
			bestHost = fields[0]
			bestLatency = latency
		}
	}
	return bestHost, bestLatency
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

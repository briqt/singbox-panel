package handler

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

type ValidateHandler struct {
	Config *ConfigHandler
}

func (h *ValidateHandler) HandleDNSCheck(w http.ResponseWriter, r *http.Request) {
	domain := r.URL.Query().Get("domain")
	expectedIP := r.URL.Query().Get("ip")
	if domain == "" {
		writeError(w, http.StatusBadRequest, "domain parameter required")
		return
	}

	ips, err := net.LookupHost(domain)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"domain":  domain,
			"valid":   false,
			"error":   "DNS lookup failed: " + err.Error(),
			"records": []string{},
		})
		return
	}

	match := false
	if expectedIP != "" {
		for _, ip := range ips {
			if ip == expectedIP {
				match = true
				break
			}
		}
	}

	result := map[string]any{
		"domain":  domain,
		"valid":   len(ips) > 0,
		"records": ips,
	}
	if expectedIP != "" {
		result["expected_ip"] = expectedIP
		result["match"] = match
		if !match {
			result["warning"] = fmt.Sprintf("domain resolves to %v but node IP is %s", ips, expectedIP)
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *ValidateHandler) HandleCertInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}

	nodeID := parseNodeIDFromConfigPath(r.URL.Path)
	if nodeID == 0 {
		writeError(w, http.StatusBadRequest, "invalid node id")
		return
	}

	node, err := h.Config.Nodes.Get(nodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}

	domain := node.Domain
	if d := r.URL.Query().Get("domain"); d != "" {
		domain = d
	}
	if domain == "" {
		writeError(w, http.StatusBadRequest, "node has no domain set; pass ?domain=xxx or update node domain first")
		return
	}

	// Verify DNS first
	ips, err := net.LookupHost(domain)
	if err != nil {
		writeError(w, http.StatusBadRequest, "DNS lookup failed for "+domain+": "+err.Error())
		return
	}
	found := false
	for _, ip := range ips {
		if ip == node.Host {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("DNS mismatch: %s resolves to %v but node IP is %s. Fix DNS first.", domain, ips, node.Host))
		return
	}

	client, err := h.Config.sshConnect(node)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ssh: "+err.Error())
		return
	}
	defer client.Close()

	// Install acme.sh if not present, then issue cert
	certDir := "/etc/sing-box/tls"
	certPath := certDir + "/" + domain + ".crt"
	keyPath := certDir + "/" + domain + ".key"

	installScript := fmt.Sprintf(`
set -e
mkdir -p %s

# Install acme.sh if missing
if ! command -v /root/.acme.sh/acme.sh &>/dev/null; then
  curl -sL https://get.acme.sh | sh -s email=admin@%s 2>&1
fi

# Issue cert using standalone mode (needs port 80 free briefly)
/root/.acme.sh/acme.sh --issue -d %s --standalone --keylength ec-256 --force 2>&1 || true

# Install cert to target dir
/root/.acme.sh/acme.sh --install-cert -d %s --ecc \
  --fullchain-file %s \
  --key-file %s \
  --reloadcmd "systemctl restart sing-box 2>/dev/null || true" 2>&1

# Verify files exist
test -f %s && test -f %s && echo "CERT_OK"
`, certDir, domain, domain, domain, certPath, keyPath, certPath, keyPath)

	out, err := sshRun(client, installScript)
	if err != nil || !strings.Contains(out, "CERT_OK") {
		writeError(w, http.StatusInternalServerError, "cert install failed: "+strings.TrimSpace(out))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "cert_installed",
		"node":      node.Name,
		"domain":    domain,
		"cert_path": certPath,
		"key_path":  keyPath,
		"message":   "Certificate installed. Use these paths in inbound settings.",
	})
}

package handler

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

type ValidateHandler struct {
	Config *ConfigHandler
}

type UploadCertReq struct {
	Domain string `json:"domain"`
	Cert   string `json:"cert"`
	Key    string `json:"key"`
}

func (h *ValidateHandler) HandleCertUpload(w http.ResponseWriter, r *http.Request) {
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

	var req UploadCertReq
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Domain == "" {
		req.Domain = node.Domain
	}
	if req.Domain == "" || req.Cert == "" || req.Key == "" {
		writeError(w, http.StatusBadRequest, "domain, cert, and key are required")
		return
	}
	if !validDomainName(req.Domain) {
		writeError(w, http.StatusBadRequest, "invalid domain")
		return
	}
	if err := validateCertificate(req.Domain, []byte(req.Cert), []byte(req.Key), time.Now()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	nodeLock := h.Config.pushLock(nodeID)
	nodeLock.Lock()
	defer nodeLock.Unlock()

	client, err := h.Config.sshConnect(node)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ssh: "+err.Error())
		return
	}
	defer client.Close()

	certDir := "/etc/sing-box/tls"
	certPath := certDir + "/" + req.Domain + ".crt"
	keyPath := certDir + "/" + req.Domain + ".key"

	if _, err := sshRun(client, "mkdir -p "+certDir); err != nil {
		writeError(w, http.StatusBadGateway, "create certificate directory: "+err.Error())
		return
	}
	if err := sshWriteFile(client, certPath, []byte(req.Cert)); err != nil {
		writeError(w, http.StatusInternalServerError, "write cert: "+err.Error())
		return
	}
	if err := sshWriteFile(client, keyPath, []byte(req.Key)); err != nil {
		writeError(w, http.StatusInternalServerError, "write key: "+err.Error())
		return
	}
	if _, err := sshRun(client, "chmod 600 "+shellQuote(keyPath)); err != nil {
		writeError(w, http.StatusBadGateway, "protect certificate key: "+err.Error())
		return
	}
	state := readCertStatuses(client, []certTarget{{Domain: req.Domain, CertPath: certPath, KeyPath: keyPath}}, time.Now())[0]
	if state == nil || state.Error != "" || state.Expired {
		writeError(w, http.StatusBadGateway, "uploaded certificate failed read-back validation")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "uploaded",
		"node":      node.Name,
		"domain":    req.Domain,
		"cert_path": certPath,
		"key_path":  keyPath,
		"cert":      state,
	})
}

func parseJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func validDomainName(domain string) bool {
	if net.ParseIP(domain) != nil || len(domain) == 0 || len(domain) > 253 || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') &&
				(char < 'A' || char > 'Z') &&
				(char < '0' || char > '9') &&
				char != '-' {
				return false
			}
		}
	}
	return true
}

func validateCertificate(domain string, certPEM, keyPEM []byte, now time.Time) error {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("invalid certificate/key pair: %w", err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("invalid certificate: %w", err)
	}
	if err := cert.VerifyHostname(domain); err != nil {
		return fmt.Errorf("certificate does not cover %s: %w", domain, err)
	}
	if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
		return fmt.Errorf("certificate is outside its validity period")
	}
	return nil
}

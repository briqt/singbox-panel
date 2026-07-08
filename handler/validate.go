package handler

import (
	"encoding/json"
	"net/http"
	"strings"
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

	client, err := h.Config.sshConnect(node)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ssh: "+err.Error())
		return
	}
	defer client.Close()

	certDir := "/etc/sing-box/tls"
	certPath := certDir + "/" + req.Domain + ".crt"
	keyPath := certDir + "/" + req.Domain + ".key"

	sshRun(client, "mkdir -p "+certDir)
	if err := sshWriteFile(client, certPath, []byte(req.Cert)); err != nil {
		writeError(w, http.StatusInternalServerError, "write cert: "+err.Error())
		return
	}
	if err := sshWriteFile(client, keyPath, []byte(req.Key)); err != nil {
		writeError(w, http.StatusInternalServerError, "write key: "+err.Error())
		return
	}
	sshRun(client, "chmod 600 "+keyPath)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "uploaded",
		"node":      node.Name,
		"domain":    req.Domain,
		"cert_path": certPath,
		"key_path":  keyPath,
	})
}

func parseJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func validDomainName(domain string) bool {
	if len(domain) == 0 || len(domain) > 253 || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
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

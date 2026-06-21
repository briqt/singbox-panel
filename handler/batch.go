package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/briqt/singbox-panel/model"
)

type BatchHandler struct {
	Users  *model.UserStore
	Nodes  *model.NodeStore
	Config *ConfigHandler
}

func (h *BatchHandler) PushAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}

	nodes, err := h.Nodes.ListEnabled()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type result struct {
		Node   string `json:"node"`
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	var results []result

	users, err := h.Users.ListEnabled()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	for _, node := range nodes {
		if node.ProxyType != "singbox" {
			results = append(results, result{Node: node.Name, Status: "skipped", Error: "not singbox"})
			continue
		}
		inbounds, err := h.Nodes.ListInbounds(node.ID)
		if err != nil {
			results = append(results, result{Node: node.Name, Status: "error", Error: err.Error()})
			continue
		}
		_ = users
		configBytes, err := h.Config.generateConfig(node.ID)
		if err != nil {
			results = append(results, result{Node: node.Name, Status: "error", Error: err.Error()})
			continue
		}
		_ = inbounds
		if err := h.Config.pushViaSSH(&node, configBytes); err != nil {
			results = append(results, result{Node: node.Name, Status: "error", Error: err.Error()})
			continue
		}
		results = append(results, result{Node: node.Name, Status: "pushed"})
	}

	writeJSON(w, http.StatusOK, results)
}

type TemplateReq struct {
	NodeID   int    `json:"node_id"`
	Template string `json:"template"`
	Domain   string `json:"domain"`
	CertPath string `json:"cert_path"`
	KeyPath  string `json:"key_path"`
}

func (h *BatchHandler) ApplyTemplate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}

	var req TemplateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	node, err := h.Nodes.Get(req.NodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}

	domain := req.Domain
	if domain == "" {
		domain = node.Domain
	}
	certPath := req.CertPath
	keyPath := req.KeyPath

	var inbounds []model.CreateInboundReq

	switch req.Template {
	case "standard-3":
		if domain == "" || certPath == "" || keyPath == "" {
			writeError(w, http.StatusBadRequest, "standard-3 template requires domain, cert_path, key_path")
			return
		}
		inbounds = templateStandard3(domain, certPath, keyPath)
	case "reality-only":
		writeError(w, http.StatusBadRequest, "reality-only template requires manual reality keys; use POST /api/nodes/{id}/inbounds directly")
		return
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown template: %s (available: standard-3)", req.Template))
		return
	}

	var created []model.NodeInbound
	for _, ib := range inbounds {
		result, err := h.Nodes.CreateInbound(node.ID, ib)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "create inbound: "+err.Error())
			return
		}
		created = append(created, *result)
	}

	writeJSON(w, http.StatusCreated, created)
}

func templateStandard3(domain, certPath, keyPath string) []model.CreateInboundReq {
	return []model.CreateInboundReq{
		{
			Tag:      "vless-vision",
			Protocol: "vless-vision",
			Port:     443,
			Settings: mustJSON(map[string]any{
				"tls_domain": domain,
				"cert_path":  certPath,
				"key_path":   keyPath,
			}),
		},
		{
			Tag:      "hysteria2",
			Protocol: "hysteria2",
			Port:     443,
			Settings: mustJSON(map[string]any{
				"tls_domain": domain,
				"cert_path":  certPath,
				"key_path":   keyPath,
				"alpn":       "h3",
			}),
		},
		{
			Tag:      "vless-reality",
			Protocol: "vless-reality",
			Port:     443,
			Settings: mustJSON(map[string]any{
				"reality_sni":         "www.google.com",
				"reality_public_key":  "NEEDS_GENERATION",
				"reality_private_key": "NEEDS_GENERATION",
				"reality_short_id":    "0123456789abcdef",
				"reality_fingerprint": "chrome",
			}),
		},
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

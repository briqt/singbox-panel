package handler

import (
	"net/http"

	"github.com/briqt/singbox-panel/model"
)

type BatchHandler struct {
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

	for _, node := range nodes {
		if node.ProxyType != "singbox" {
			results = append(results, result{Node: node.Name, Status: "skipped", Error: "not singbox"})
			continue
		}
		configBytes, err := h.Config.generateConfig(node.ID)
		if err != nil {
			results = append(results, result{Node: node.Name, Status: "error", Error: err.Error()})
			continue
		}
		if err := h.Config.pushViaSSH(&node, configBytes); err != nil {
			results = append(results, result{Node: node.Name, Status: "error", Error: err.Error()})
			continue
		}
		results = append(results, result{Node: node.Name, Status: "pushed"})
	}

	writeJSON(w, http.StatusOK, results)
}

func (h *BatchHandler) ApplyTemplate(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusGone, "use POST /api/nodes/{id}/auto-setup instead")
}

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

	nodeIDs := make([]int, 0, len(nodes))
	for _, node := range nodes {
		nodeIDs = append(nodeIDs, node.ID)
	}
	writeJSON(w, http.StatusOK, h.Config.SyncNodes(nodeIDs))
}

func (h *BatchHandler) ApplyTemplate(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusGone, "use POST /api/nodes/{id}/auto-setup instead")
}

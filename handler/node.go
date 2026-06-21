package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/briqt/singbox-panel/model"
)

type NodeHandler struct {
	Store *model.NodeStore
}

func (h *NodeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	switch {
	case r.Method == http.MethodGet && path == "/api/nodes":
		h.list(w, r)
	case r.Method == http.MethodPost && path == "/api/nodes":
		h.create(w, r)
	case r.Method == http.MethodGet && matchNodePath(path) && !strings.Contains(path, "/inbounds"):
		h.get(w, r)
	case r.Method == http.MethodPut && matchNodePath(path) && !strings.Contains(path, "/inbounds"):
		h.update(w, r)
	case r.Method == http.MethodDelete && matchNodePath(path) && !strings.Contains(path, "/inbounds"):
		h.delete(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/inbounds"):
		h.createInbound(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/api/inbounds/"):
		h.deleteInbound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func matchNodePath(path string) bool {
	return strings.HasPrefix(path, "/api/nodes/")
}

func (h *NodeHandler) list(w http.ResponseWriter, _ *http.Request) {
	nodes, err := h.Store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, nodes)
}

func (h *NodeHandler) create(w http.ResponseWriter, r *http.Request) {
	var req model.CreateNodeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Name == "" || req.Host == "" {
		writeError(w, http.StatusBadRequest, "name and host are required")
		return
	}
	node, err := h.Store.Create(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, node)
}

func (h *NodeHandler) get(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.URL.Path, "/api/nodes/")
	if id == 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	nwi, err := h.Store.GetWithInbounds(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	writeJSON(w, http.StatusOK, nwi)
}

func (h *NodeHandler) update(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.URL.Path, "/api/nodes/")
	if id == 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req model.UpdateNodeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	node, err := h.Store.Update(id, req)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	writeJSON(w, http.StatusOK, node)
}

func (h *NodeHandler) delete(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.URL.Path, "/api/nodes/")
	if id == 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.Store.Delete(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *NodeHandler) createInbound(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/nodes/"), "/")
	if len(parts) < 2 {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}
	nodeID := parseID("/api/nodes/"+parts[0], "/api/nodes/")
	if nodeID == 0 {
		writeError(w, http.StatusBadRequest, "invalid node id")
		return
	}
	var req model.CreateInboundReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Protocol == "" || req.Port == 0 {
		writeError(w, http.StatusBadRequest, "protocol and port are required")
		return
	}
	ib, err := h.Store.CreateInbound(nodeID, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, ib)
}

func (h *NodeHandler) deleteInbound(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.URL.Path, "/api/inbounds/")
	if id == 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.Store.DeleteInbound(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

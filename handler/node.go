package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/briqt/singbox-panel/model"
)

type NodeHandler struct {
	Store  *model.NodeStore
	Access *model.AccessStore
	Sync   NodeSynchronizer
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
	if req.Domain != "" && !validDomainName(req.Domain) {
		writeError(w, http.StatusBadRequest, "invalid domain")
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
	current, err := h.Store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	if req.Domain != nil && *req.Domain != current.Domain {
		if *req.Domain != "" && !validDomainName(*req.Domain) {
			writeError(w, http.StatusBadRequest, "invalid domain")
			return
		}
		inbounds, err := h.Store.ListInbounds(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, inbound := range inbounds {
			if inbound.Protocol == "hysteria2" || inbound.Protocol == "vless-httpupgrade" {
				writeError(w, http.StatusConflict, "node has domain-bound inbounds; migrate the domain through auto-setup")
				return
			}
		}
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
	node, err := h.Store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	if node.ProxyType != "singbox" {
		writeError(w, http.StatusConflict, "automatic remote decommission is only supported for singbox nodes")
		return
	}
	inbounds, err := h.Store.ListInbounds(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	accessCount, err := h.Access.CountForNode(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(inbounds) > 0 || accessCount > 0 {
		writeError(w, http.StatusConflict, "node still has inbounds or user access; remove them before deleting the node")
		return
	}
	results := syncNodes(h.Sync, []int{id})
	if err := syncFailure(results); err != nil {
		writeError(w, http.StatusBadGateway, "refusing to delete node before remote decommission: "+err.Error())
		return
	}
	if err := h.Store.Delete(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "node": node.Name, "sync": results})
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
	node, err := h.Store.Get(nodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	if node.ProxyType != "singbox" {
		writeError(w, http.StatusBadRequest, "inbound synchronization is only supported for singbox nodes")
		return
	}
	var req model.CreateInboundReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Protocol == "" || req.Port < 1 || req.Port > 65535 {
		writeError(w, http.StatusBadRequest, "protocol and a valid port are required")
		return
	}
	var settings map[string]any
	if err := json.Unmarshal(req.Settings, &settings); err != nil {
		writeError(w, http.StatusBadRequest, "settings must be a JSON object")
		return
	}
	switch req.Protocol {
	case "hysteria2":
		domain, _ := settings["domain"].(string)
		if !validDomainName(domain) {
			writeError(w, http.StatusBadRequest, "hysteria2 requires domain in settings")
			return
		}
		certPath, _ := settings["cert_path"].(string)
		keyPath, _ := settings["key_path"].(string)
		if certPath == "" || keyPath == "" {
			writeError(w, http.StatusBadRequest, "hysteria2 requires cert_path and key_path in settings")
			return
		}
	case "vless-httpupgrade":
		domain, _ := settings["domain"].(string)
		if !validDomainName(domain) {
			writeError(w, http.StatusBadRequest, "vless-httpupgrade requires domain in settings")
			return
		}
		certPath, _ := settings["cert_path"].(string)
		keyPath, _ := settings["key_path"].(string)
		if certPath == "" || keyPath == "" {
			writeError(w, http.StatusBadRequest, "vless-httpupgrade requires cert_path and key_path in settings")
			return
		}
	case "vless-reality":
		sni, _ := settings["sni"].(string)
		privateKey, _ := settings["private_key"].(string)
		publicKey, _ := settings["public_key"].(string)
		if !validDomainName(sni) || privateKey == "" || publicKey == "" {
			writeError(w, http.StatusBadRequest, "vless-reality requires sni, private_key, public_key in settings")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "unsupported protocol")
		return
	}
	ib, err := h.Store.CreateInbound(nodeID, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	results := syncNodes(h.Sync, []int{nodeID})
	if err := syncFailure(results); err != nil {
		if rollbackErr := h.Store.DeleteInbound(ib.ID); rollbackErr != nil {
			writeError(w, http.StatusInternalServerError, "node sync failed and inbound rollback failed: "+rollbackErr.Error())
			return
		}
		restoreResults := syncNodes(h.Sync, []int{nodeID})
		if restoreErr := syncFailure(restoreResults); restoreErr != nil {
			writeError(w, http.StatusBadGateway, "inbound creation was rolled back, but restoring the node failed: "+restoreErr.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "inbound was not created: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, inboundSyncResponse{NodeInbound: ib, Sync: results})
}

func (h *NodeHandler) deleteInbound(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.URL.Path, "/api/inbounds/")
	if id == 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	inbound, err := h.Store.GetInbound(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "inbound not found")
		return
	}
	node, err := h.Store.Get(inbound.NodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	if node.ProxyType != "singbox" {
		writeError(w, http.StatusBadRequest, "inbound synchronization is only supported for singbox nodes")
		return
	}
	if err := h.Store.DeleteInbound(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	results := syncNodes(h.Sync, []int{inbound.NodeID})
	if err := syncFailure(results); err != nil {
		_, restoreErr := h.Store.RestoreInbound(*inbound)
		if restoreErr != nil {
			writeError(w, http.StatusInternalServerError, "delete sync failed and database restore failed: "+restoreErr.Error())
			return
		}
		restoreResults := syncNodes(h.Sync, []int{inbound.NodeID})
		if restoreErr := syncFailure(restoreResults); restoreErr != nil {
			writeError(w, http.StatusBadGateway, "inbound deletion was rolled back, but restoring the node failed: "+restoreErr.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "inbound deletion was rolled back: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "deleted",
		"inbound": map[string]any{
			"id": inbound.ID, "protocol": inbound.Protocol, "port": inbound.Port,
		},
		"sync": results,
	})
}

func (h *NodeHandler) Reorder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.NotFound(w, r)
		return
	}
	var items []model.ReorderItem
	if err := json.NewDecoder(r.Body).Decode(&items); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := h.Store.ReorderNodes(items); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *NodeHandler) ReorderInbounds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.NotFound(w, r)
		return
	}
	var items []model.ReorderItem
	if err := json.NewDecoder(r.Body).Decode(&items); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	nodeID := parseID(r.URL.Path, "/api/nodes/")
	if nodeID == 0 {
		writeError(w, http.StatusBadRequest, "invalid node id")
		return
	}
	node, err := h.Store.Get(nodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	if node.ProxyType != "singbox" {
		writeError(w, http.StatusBadRequest, "inbound synchronization is only supported for singbox nodes")
		return
	}
	before, err := h.Store.ListInbounds(nodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.Store.ReorderInbounds(nodeID, items); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	results := syncNodes(h.Sync, []int{nodeID})
	if err := syncFailure(results); err != nil {
		rollback := make([]model.ReorderItem, 0, len(before))
		for _, inbound := range before {
			rollback = append(rollback, model.ReorderItem{ID: inbound.ID, SortOrder: inbound.SortOrder})
		}
		if rollbackErr := h.Store.ReorderInbounds(nodeID, rollback); rollbackErr != nil {
			writeError(w, http.StatusInternalServerError, "node sync failed and inbound order rollback failed: "+rollbackErr.Error())
			return
		}
		restoreResults := syncNodes(h.Sync, []int{nodeID})
		if restoreErr := syncFailure(restoreResults); restoreErr != nil {
			writeError(w, http.StatusBadGateway, "inbound reorder was rolled back, but restoring the node failed: "+restoreErr.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "inbound reorder was rolled back: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "sync": results})
}

type inboundSyncResponse struct {
	*model.NodeInbound
	Sync []NodeSyncResult `json:"sync"`
}

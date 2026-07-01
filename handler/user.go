package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/briqt/singbox-panel/model"
)

type UserHandler struct {
	Store  *model.UserStore
	Access *model.AccessStore
	Sync   NodeSynchronizer
}

func (h *UserHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/users":
		h.list(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/users":
		h.create(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/users/") && !strings.Contains(r.URL.Path, "/reset"):
		h.get(w, r)
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/users/") && !strings.Contains(r.URL.Path, "/reset"):
		h.update(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/users/"):
		h.delete(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reset-traffic"):
		h.resetTraffic(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reset-sub-token"):
		h.resetSubToken(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *UserHandler) list(w http.ResponseWriter, _ *http.Request) {
	users, err := h.Store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (h *UserHandler) create(w http.ResponseWriter, r *http.Request) {
	var req model.CreateUserReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	user, err := h.Store.Create(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (h *UserHandler) get(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.URL.Path, "/api/users/")
	if id == 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	user, err := h.Store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (h *UserHandler) update(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.URL.Path, "/api/users/")
	if id == 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	before, err := h.Store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	beforeNodeIDs, err := h.Access.ListNodeIDs(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var req struct {
		model.UpdateUserReq
		NodeIDs *[]int `json:"node_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	var user *model.User
	if req.NodeIDs != nil {
		user, err = h.Store.UpdateWithAccess(id, req.UpdateUserReq, *req.NodeIDs)
	} else {
		user, err = h.Store.Update(id, req.UpdateUserReq)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	afterNodeIDs, err := h.Access.ListNodeIDs(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	targetNodeIDs := changedIDs(beforeNodeIDs, afterNodeIDs)
	if req.NodeIDs != nil || userConfigChanged(before, user) {
		targetNodeIDs = unionIDs(beforeNodeIDs, afterNodeIDs)
	}
	writeJSON(w, http.StatusOK, userSyncResponse{
		User: user,
		Sync: syncNodes(h.Sync, targetNodeIDs),
	})
}

func (h *UserHandler) delete(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.URL.Path, "/api/users/")
	if id == 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	nodeIDs, err := h.Access.ListNodeIDs(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.Store.Delete(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.Access.RevokeAll(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "deleted",
		"sync":   syncNodes(h.Sync, nodeIDs),
	})
}

func (h *UserHandler) resetTraffic(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.URL.Path, "/api/users/")
	if id == 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.Store.ResetTraffic(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, _ := h.Store.Get(id)
	nodeIDs, _ := h.Access.ListNodeIDs(id)
	writeJSON(w, http.StatusOK, userSyncResponse{
		User: user,
		Sync: syncNodes(h.Sync, nodeIDs),
	})
}

type userSyncResponse struct {
	*model.User
	Sync []NodeSyncResult `json:"sync"`
}

func userConfigChanged(before, after *model.User) bool {
	return before.Name != after.Name ||
		before.Enabled != after.Enabled ||
		before.TrafficLimitBytes != after.TrafficLimitBytes ||
		before.ExpireAt != after.ExpireAt
}

func (h *UserHandler) resetSubToken(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.URL.Path, "/api/users/")
	if id == 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	token, err := h.Store.ResetSubToken(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sub_token": token})
}

func parseID(path, prefix string) int {
	s := strings.TrimPrefix(path, prefix)
	s = strings.Split(s, "/")[0]
	id, _ := strconv.Atoi(s)
	return id
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

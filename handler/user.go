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
		h.restoreUser(before, beforeNodeIDs)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	targetNodeIDs := changedIDs(beforeNodeIDs, afterNodeIDs)
	if req.NodeIDs != nil || userConfigChanged(before, user) {
		targetNodeIDs = unionIDs(beforeNodeIDs, afterNodeIDs)
	}
	results := syncNodes(h.Sync, targetNodeIDs)
	if syncErr := syncFailure(results); syncErr != nil {
		if rollbackErr := h.restoreUser(before, beforeNodeIDs); rollbackErr != nil {
			writeError(w, http.StatusInternalServerError, "node sync failed and user rollback failed: "+rollbackErr.Error())
			return
		}
		restoreResults := syncNodes(h.Sync, targetNodeIDs)
		if restoreErr := syncFailure(restoreResults); restoreErr != nil {
			writeError(w, http.StatusBadGateway, "user edit was rolled back, but restoring one or more nodes failed: "+restoreErr.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "user edit was rolled back: "+syncErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, userSyncResponse{
		User: user,
		Sync: results,
	})
}

func (h *UserHandler) delete(w http.ResponseWriter, r *http.Request) {
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
	nodeIDs, err := h.Access.ListNodeIDs(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	disabled := false
	if _, err := h.Store.UpdateWithAccess(id, model.UpdateUserReq{Enabled: &disabled}, nil); err != nil {
		writeError(w, http.StatusInternalServerError, "prepare user deletion: "+err.Error())
		return
	}
	results := syncNodes(h.Sync, nodeIDs)
	if syncErr := syncFailure(results); syncErr != nil {
		if rollbackErr := h.restoreUser(before, nodeIDs); rollbackErr != nil {
			writeError(w, http.StatusInternalServerError, "node sync failed and user rollback failed: "+rollbackErr.Error())
			return
		}
		restoreResults := syncNodes(h.Sync, nodeIDs)
		if restoreErr := syncFailure(restoreResults); restoreErr != nil {
			writeError(w, http.StatusBadGateway, "user deletion was rolled back, but restoring one or more nodes failed: "+restoreErr.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "user deletion was rolled back: "+syncErr.Error())
		return
	}
	if err := h.Store.DeleteWithRelatedData(id); err != nil {
		if rollbackErr := h.restoreUser(before, nodeIDs); rollbackErr != nil {
			writeError(w, http.StatusInternalServerError, "delete user failed and rollback failed: "+rollbackErr.Error())
			return
		}
		restoreResults := syncNodes(h.Sync, nodeIDs)
		if restoreErr := syncFailure(restoreResults); restoreErr != nil {
			writeError(w, http.StatusBadGateway, "user deletion was rolled back, but restoring one or more nodes failed: "+restoreErr.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "delete user: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "deleted",
		"sync":   results,
	})
}

func (h *UserHandler) resetTraffic(w http.ResponseWriter, r *http.Request) {
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
	nodeIDs, err := h.Access.ListNodeIDs(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.Store.ResetTraffic(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, err := h.Store.Get(id)
	if err != nil {
		h.Store.RestoreTraffic(*before)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	results := syncNodes(h.Sync, nodeIDs)
	if syncErr := syncFailure(results); syncErr != nil {
		if rollbackErr := h.Store.RestoreTraffic(*before); rollbackErr != nil {
			writeError(w, http.StatusInternalServerError, "node sync failed and traffic rollback failed: "+rollbackErr.Error())
			return
		}
		restoreResults := syncNodes(h.Sync, nodeIDs)
		if restoreErr := syncFailure(restoreResults); restoreErr != nil {
			writeError(w, http.StatusBadGateway, "traffic reset was rolled back, but restoring one or more nodes failed: "+restoreErr.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "traffic reset was rolled back: "+syncErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, userSyncResponse{
		User: user,
		Sync: results,
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

func (h *UserHandler) restoreUser(user *model.User, nodeIDs []int) error {
	name := user.Name
	enabled := user.Enabled
	trafficLimit := user.TrafficLimitBytes
	trafficResetDay := user.TrafficResetDay
	expireAt := user.ExpireAt
	_, err := h.Store.UpdateWithAccess(user.ID, model.UpdateUserReq{
		Name:              &name,
		Enabled:           &enabled,
		TrafficLimitBytes: &trafficLimit,
		TrafficResetDay:   &trafficResetDay,
		ExpireAt:          &expireAt,
	}, nodeIDs)
	return err
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

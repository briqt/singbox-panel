package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/briqt/singbox-panel/model"
)

type AuthHandler struct {
	Users      *model.UserStore
	AdminToken string
}

type RegisterReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *AuthHandler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var req RegisterReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	if len(req.Password) < 6 {
		writeError(w, http.StatusBadRequest, "password must be at least 6 characters")
		return
	}

	passHash := hashPassword(req.Password)
	user, err := h.Users.CreateWithPassword(req.Username, passHash)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, http.StatusConflict, "username already taken")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":        user.ID,
		"name":      user.Name,
		"sub_token": user.SubToken,
		"message":   "registered successfully, waiting for admin approval",
	})
}

type AccessHandler struct {
	Access *model.AccessStore
	Nodes  *model.NodeStore
}

func (h *AccessHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/users/") && strings.HasSuffix(path, "/access"):
		h.listAccess(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/api/users/") && strings.HasSuffix(path, "/access"):
		h.grantAccess(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/api/users/") && strings.HasSuffix(path, "/access"):
		h.revokeAccess(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *AccessHandler) listAccess(w http.ResponseWriter, r *http.Request) {
	userID := extractUserID(r.URL.Path)
	if userID == 0 {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	nodeIDs, err := h.Access.ListNodeIDs(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var nodes []model.Node
	for _, nid := range nodeIDs {
		n, err := h.Nodes.Get(nid)
		if err == nil {
			nodes = append(nodes, *n)
		}
	}
	writeJSON(w, http.StatusOK, nodes)
}

type AccessReq struct {
	NodeID int    `json:"node_id"`
	All    bool   `json:"all"`
}

func (h *AccessHandler) grantAccess(w http.ResponseWriter, r *http.Request) {
	userID := extractUserID(r.URL.Path)
	if userID == 0 {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var req AccessReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.All {
		h.Access.GrantAll(userID)
	} else if req.NodeID > 0 {
		h.Access.Grant(userID, req.NodeID)
	} else {
		writeError(w, http.StatusBadRequest, "specify node_id or all:true")
		return
	}
	nodeIDs, _ := h.Access.ListNodeIDs(userID)
	writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "accessible_nodes": nodeIDs})
}

func (h *AccessHandler) revokeAccess(w http.ResponseWriter, r *http.Request) {
	userID := extractUserID(r.URL.Path)
	if userID == 0 {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var req AccessReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.All {
		h.Access.RevokeAll(userID)
	} else if req.NodeID > 0 {
		h.Access.Revoke(userID, req.NodeID)
	}
	nodeIDs, _ := h.Access.ListNodeIDs(userID)
	writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "accessible_nodes": nodeIDs})
}

func extractUserID(path string) int {
	// /api/users/123/access
	path = strings.TrimPrefix(path, "/api/users/")
	parts := strings.Split(path, "/")
	if len(parts) < 1 {
		return 0
	}
	id, _ := strconv.Atoi(parts[0])
	return id
}

func hashPassword(password string) string {
	h := sha256.Sum256([]byte(password))
	return hex.EncodeToString(h[:])
}

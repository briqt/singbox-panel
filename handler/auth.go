package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/briqt/singbox-panel/model"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type contextKey string

const (
	ctxRole   contextKey = "role"
	ctxUserID contextKey = "user_id"
)

type AuthHandler struct {
	Users     *model.UserStore
	AdminUser string
	AdminPass string
	JWTSecret string
}

type LoginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type RegisterReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *AuthHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var req LoginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password required")
		return
	}

	// Check admin credentials
	if req.Username == h.AdminUser && h.AdminPass != "" && req.Password == h.AdminPass {
		token := h.generateJWT(0, "admin", req.Username)
		writeJSON(w, http.StatusOK, map[string]any{"token": token, "role": "admin", "username": req.Username})
		return
	}

	// Check user credentials
	user, err := h.Users.GetByUsername(req.Username)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if !user.Enabled {
		writeError(w, http.StatusForbidden, "account not yet approved by admin")
		return
	}
	token := h.generateJWT(user.ID, "user", user.Name)
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "role": "user", "username": user.Name, "user_id": user.ID})
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

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "password hash failed")
		return
	}
	user, err := h.Users.CreateWithPassword(req.Username, string(hash))
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

func (h *AuthHandler) generateJWT(userID int, role, username string) string {
	claims := jwt.MapClaims{
		"user_id":  userID,
		"role":     role,
		"username": username,
		"exp":      time.Now().Add(7 * 24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, _ := token.SignedString([]byte(h.JWTSecret))
	return signed
}

func (h *AuthHandler) JWTAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		tokenStr := strings.TrimPrefix(auth, "Bearer ")

		// Legacy: support old ADMIN_TOKEN for backward compat
		if h.AdminPass == "" && tokenStr == h.JWTSecret {
			ctx := context.WithValue(r.Context(), ctxRole, "admin")
			ctx = context.WithValue(ctx, ctxUserID, 0)
			next(w, r.WithContext(ctx))
			return
		}

		token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
			return []byte(h.JWTSecret), nil
		})
		if err != nil || !token.Valid {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			writeError(w, http.StatusUnauthorized, "invalid token claims")
			return
		}
		role, _ := claims["role"].(string)
		userIDf, _ := claims["user_id"].(float64)
		ctx := context.WithValue(r.Context(), ctxRole, role)
		ctx = context.WithValue(ctx, ctxUserID, int(userIDf))
		next(w, r.WithContext(ctx))
	}
}

func (h *AuthHandler) AdminOnly(next http.HandlerFunc) http.HandlerFunc {
	return h.JWTAuth(func(w http.ResponseWriter, r *http.Request) {
		role, _ := r.Context().Value(ctxRole).(string)
		if role != "admin" {
			writeError(w, http.StatusForbidden, "admin access required")
			return
		}
		next(w, r)
	})
}

// User-facing endpoints

type MeHandler struct {
	Users  *model.UserStore
	Nodes  *model.NodeStore
	Access *model.AccessStore
}

func (h *MeHandler) HandleMe(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(int)
	role, _ := r.Context().Value(ctxRole).(string)
	if role == "admin" {
		writeJSON(w, http.StatusOK, map[string]any{"role": "admin"})
		return
	}
	user, err := h.Users.Get(userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": user.ID, "name": user.Name, "role": "user",
		"sub_token": user.SubToken, "enabled": user.Enabled,
		"traffic_used_bytes": user.TrafficUsedBytes, "traffic_limit_bytes": user.TrafficLimitBytes,
		"expire_at": user.ExpireAt,
	})
}

func (h *MeHandler) HandleMyNodes(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(int)
	role, _ := r.Context().Value(ctxRole).(string)
	if role == "admin" {
		nodes, _ := h.Nodes.List()
		writeJSON(w, http.StatusOK, nodes)
		return
	}
	nodeIDs, _ := h.Access.ListNodeIDs(userID)
	var nodes []map[string]any
	for _, nid := range nodeIDs {
		n, err := h.Nodes.Get(nid)
		if err != nil || !n.Enabled {
			continue
		}
		nodes = append(nodes, map[string]any{
			"id": n.ID, "name": n.Name, "domain": n.Domain,
		})
	}
	writeJSON(w, http.StatusOK, nodes)
}

// AccessHandler (admin-only, unchanged)

type AccessHandler struct {
	Access *model.AccessStore
	Nodes  *model.NodeStore
	Sync   NodeSynchronizer
}

func (h *AccessHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/users/") && strings.HasSuffix(path, "/access"):
		h.listAccess(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/api/users/") && strings.HasSuffix(path, "/access"):
		h.grantAccess(w, r)
	case r.Method == http.MethodPut && strings.HasPrefix(path, "/api/users/") && strings.HasSuffix(path, "/access"):
		h.replaceAccess(w, r)
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
	NodeID int  `json:"node_id"`
	All    bool `json:"all"`
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
	before, err := h.Access.ListNodeIDs(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.All {
		err = h.Access.GrantAll(userID)
	} else if req.NodeID > 0 {
		err = h.Access.Grant(userID, req.NodeID)
	} else {
		writeError(w, http.StatusBadRequest, "specify node_id or all:true")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	nodeIDs, err := h.Access.ListNodeIDs(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":          userID,
		"accessible_nodes": nodeIDs,
		"sync":             syncNodes(h.Sync, changedIDs(before, nodeIDs)),
	})
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
	before, err := h.Access.ListNodeIDs(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.All {
		err = h.Access.RevokeAll(userID)
	} else if req.NodeID > 0 {
		err = h.Access.Revoke(userID, req.NodeID)
	} else {
		writeError(w, http.StatusBadRequest, "specify node_id or all:true")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	nodeIDs, err := h.Access.ListNodeIDs(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":          userID,
		"accessible_nodes": nodeIDs,
		"sync":             syncNodes(h.Sync, changedIDs(before, nodeIDs)),
	})
}

func (h *AccessHandler) replaceAccess(w http.ResponseWriter, r *http.Request) {
	userID := extractUserID(r.URL.Path)
	if userID == 0 {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var req struct {
		NodeIDs []int `json:"node_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	before, err := h.Access.ListNodeIDs(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.Access.Replace(userID, req.NodeIDs); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	nodeIDs, err := h.Access.ListNodeIDs(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":          userID,
		"accessible_nodes": nodeIDs,
		"sync":             syncNodes(h.Sync, changedIDs(before, nodeIDs)),
	})
}

func extractUserID(path string) int {
	path = strings.TrimPrefix(path, "/api/users/")
	parts := strings.Split(path, "/")
	if len(parts) < 1 {
		return 0
	}
	id, _ := strconv.Atoi(parts[0])
	return id
}

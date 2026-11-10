package handler

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
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
	case r.Method == http.MethodPut && strings.HasPrefix(path, "/api/inbounds/"):
		h.updateInbound(w, r)
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
	// 手动线路永远不由 auto-setup 接管，即使调用方显式传入 false。
	req.Custom = true
	inbounds, err := h.Store.ListInbounds(nodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.Protocol == "vless-reality" && len(req.Settings) == 0 {
		req.Settings, err = newRealitySettings(node, inbounds)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := validateInboundRequest(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := checkInboundConflicts(inbounds, 0, req); err != nil {
		writeError(w, http.StatusConflict, err.Error())
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

func (h *NodeHandler) updateInbound(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.URL.Path, "/api/inbounds/")
	if id == 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	before, err := h.Store.GetInbound(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "inbound not found")
		return
	}
	node, err := h.Store.Get(before.NodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	if node.ProxyType != "singbox" {
		writeError(w, http.StatusBadRequest, "inbound synchronization is only supported for singbox nodes")
		return
	}
	var patch struct {
		Name     *string         `json:"name"`
		Tag      *string         `json:"tag"`
		Protocol *string         `json:"protocol"`
		Port     *int            `json:"port"`
		Settings json.RawMessage `json:"settings"`
		Outbound json.RawMessage `json:"outbound"`
	}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	req := inboundRequest(*before)
	if patch.Name != nil {
		req.Name = *patch.Name
	}
	if patch.Tag != nil {
		req.Tag = *patch.Tag
	}
	if patch.Protocol != nil && *patch.Protocol != before.Protocol {
		writeError(w, http.StatusBadRequest, "protocol cannot be changed; create a separate inbound")
		return
	}
	if patch.Port != nil {
		req.Port = *patch.Port
	}
	if len(patch.Outbound) > 0 {
		// 显式传入的出口整体替换而非合并，避免将旧代理凭据
		// 泄露给未配置凭据的新服务器。
		req.Outbound = nil
		if err := json.Unmarshal(patch.Outbound, &req.Outbound); err != nil {
			writeError(w, http.StatusBadRequest, "invalid outbound")
			return
		}
	}
	if len(patch.Settings) > 0 {
		req.Settings, err = mergeInboundSettings(before.Settings, patch.Settings)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := validateInboundRequest(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	inbounds, err := h.Store.ListInbounds(before.NodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := checkInboundConflicts(inbounds, id, req); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	inbound, err := h.Store.UpdateInbound(id, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	results := syncNodes(h.Sync, []int{before.NodeID})
	if err := syncFailure(results); err != nil {
		if _, rollbackErr := h.Store.UpdateInbound(id, inboundRequest(*before)); rollbackErr != nil {
			writeError(w, http.StatusInternalServerError, "node sync failed and inbound rollback failed: "+rollbackErr.Error())
			return
		}
		restoreResults := syncNodes(h.Sync, []int{before.NodeID})
		if restoreErr := syncFailure(restoreResults); restoreErr != nil {
			writeError(w, http.StatusBadGateway, "inbound update was rolled back, but restoring the node failed: "+restoreErr.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "inbound update was rolled back: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, inboundSyncResponse{NodeInbound: inbound, Sync: results})
}

// inboundRequest 也用于 auto-setup 和目标切换：复制入站时
// 不得静默重置其显示名称、出口或管理归属。
func inboundRequest(inbound model.NodeInbound) model.CreateInboundReq {
	req := model.CreateInboundReq{
		Name: inbound.Name, Tag: inbound.Tag, Protocol: inbound.Protocol,
		Port: inbound.Port, Settings: inbound.Settings, Custom: inbound.Custom,
	}
	if inbound.Outbound != nil {
		outbound := *inbound.Outbound
		req.Outbound = &outbound
	}
	return req
}

var inboundIdentityFields = []string{"private_key", "public_key", "short_id", "uuid", "password", "obfs_password"}

func mergeInboundSettings(original, patch json.RawMessage) (json.RawMessage, error) {
	var settings, updates map[string]any
	if err := json.Unmarshal(original, &settings); err != nil || settings == nil {
		return nil, fmt.Errorf("stored settings must be a JSON object")
	}
	if err := json.Unmarshal(patch, &updates); err != nil || updates == nil {
		return nil, fmt.Errorf("settings must be a JSON object")
	}
	for _, key := range inboundIdentityFields {
		if value, supplied := updates[key]; supplied && !reflect.DeepEqual(value, settings[key]) {
			return nil, fmt.Errorf("%s cannot be changed on an existing inbound", key)
		}
	}
	for key, value := range updates {
		settings[key] = value
	}
	return json.Marshal(settings)
}

func validateInboundRequest(req *model.CreateInboundReq) error {
	if strings.TrimSpace(req.Tag) == "" {
		return fmt.Errorf("tag is required")
	}
	if req.Protocol == "" || req.Port < 1 || req.Port > 65535 {
		return fmt.Errorf("protocol and a valid port are required")
	}
	if req.Outbound != nil && req.Outbound.Network == "" {
		req.Outbound.Network = "tcp"
	}
	if err := model.ValidateOutbound(req.Outbound); err != nil {
		return err
	}
	var settings map[string]any
	if err := json.Unmarshal(req.Settings, &settings); err != nil || settings == nil {
		return fmt.Errorf("settings must be a JSON object")
	}
	switch req.Protocol {
	case "hysteria2", "vless-httpupgrade":
		domain, _ := settings["domain"].(string)
		if !validDomainName(domain) {
			return fmt.Errorf("%s requires domain in settings", req.Protocol)
		}
		certPath, _ := settings["cert_path"].(string)
		keyPath, _ := settings["key_path"].(string)
		if certPath == "" || keyPath == "" {
			return fmt.Errorf("%s requires cert_path and key_path in settings", req.Protocol)
		}
	case "vless-reality":
		sni, _ := settings["sni"].(string)
		privateKey, _ := settings["private_key"].(string)
		publicKey, _ := settings["public_key"].(string)
		if !validDomainName(sni) || privateKey == "" || publicKey == "" {
			return fmt.Errorf("vless-reality requires sni, private_key, public_key in settings")
		}
	default:
		return fmt.Errorf("unsupported protocol")
	}
	return nil
}

func inboundNetwork(protocol string) string {
	if protocol == "hysteria2" {
		return "udp"
	}
	return "tcp"
}

func checkInboundConflicts(inbounds []model.NodeInbound, exceptID int, req model.CreateInboundReq) error {
	for _, inbound := range inbounds {
		if inbound.ID == exceptID {
			continue
		}
		if inbound.Tag == req.Tag {
			return fmt.Errorf("inbound tag %q already exists on this node", req.Tag)
		}
		if inbound.Port == req.Port && inboundNetwork(inbound.Protocol) == inboundNetwork(req.Protocol) {
			return fmt.Errorf("port %d/%s is already used by inbound %q", req.Port, inboundNetwork(req.Protocol), inbound.Tag)
		}
	}
	return nil
}

func newRealitySettings(node *model.Node, inbounds []model.NodeInbound) (json.RawMessage, error) {
	// 只复用伪装目标，新线路使用独立的密码学身份；
	// 用户凭据由授权模块填充。
	sni := realitySNIs[0]
	defaultInbound, err := selectRealityInbound(inbounds, 0)
	if err != nil {
		return nil, err
	}
	if defaultInbound != nil && validDomainName(realitySNIOf(*defaultInbound)) {
		sni = realitySNIOf(*defaultInbound)
	}
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate Reality keypair: %w", err)
	}
	shortID := make([]byte, 8)
	if _, err := rand.Read(shortID); err != nil {
		return nil, fmt.Errorf("generate Reality short ID: %w", err)
	}
	return json.Marshal(map[string]any{
		"sni": sni, "handshake_server": sni, "handshake_port": 443,
		"private_key": base64.RawURLEncoding.EncodeToString(key.Bytes()),
		"public_key":  base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
		"short_id":    hex.EncodeToString(shortID), "fingerprint": "chrome", "server": node.Host,
	})
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
	// 删除记录也会清除该入站的授权，操作前先保存快照，
	// 避免推送失败后的回滚意外撤销用户访问权限。
	userIDs, err := h.inboundAccessSnapshot(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
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
		if restoreErr := h.restoreInboundAccess(id, userIDs); restoreErr != nil {
			writeError(w, http.StatusInternalServerError, "delete sync failed and access restore failed: "+restoreErr.Error())
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

func (h *NodeHandler) inboundAccessSnapshot(inboundID int) ([]int, error) {
	rows, err := h.Store.DB.Query(`SELECT user_id FROM user_inbound_access WHERE inbound_id = ? ORDER BY user_id`, inboundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var userIDs []int
	for rows.Next() {
		var userID int
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		userIDs = append(userIDs, userID)
	}
	return userIDs, rows.Err()
}

func (h *NodeHandler) restoreInboundAccess(inboundID int, userIDs []int) error {
	tx, err := h.Store.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, userID := range userIDs {
		if _, err := tx.Exec(`INSERT INTO user_inbound_access (user_id, inbound_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, userID, inboundID); err != nil {
			return err
		}
	}
	return tx.Commit()
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

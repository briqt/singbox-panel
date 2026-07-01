package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/briqt/singbox-panel/model"
	"github.com/briqt/singbox-panel/singbox"
	"golang.org/x/crypto/ssh"
)

type ConfigHandler struct {
	Users      *model.UserStore
	Nodes      *model.NodeStore
	Access     *model.AccessStore
	SSHKeyPath string

	pushLocksMu sync.Mutex
	pushLocks   map[int]*sync.Mutex
}

type NodeSyncResult struct {
	NodeID int    `json:"node_id"`
	Node   string `json:"node"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func (h *ConfigHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/generate"):
		h.generate(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/push"):
		h.push(w, r)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/raw-config"):
		h.getRawConfig(w, r)
	case r.Method == http.MethodPut && strings.HasSuffix(path, "/raw-config"):
		h.putRawConfig(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *ConfigHandler) generate(w http.ResponseWriter, r *http.Request) {
	nodeID := parseNodeIDFromConfigPath(r.URL.Path)
	if nodeID == 0 {
		writeError(w, http.StatusBadRequest, "invalid node id")
		return
	}
	configBytes, err := h.generateConfig(nodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(configBytes)
}

func (h *ConfigHandler) push(w http.ResponseWriter, r *http.Request) {
	nodeID := parseNodeIDFromConfigPath(r.URL.Path)
	if nodeID == 0 {
		writeError(w, http.StatusBadRequest, "invalid node id")
		return
	}
	node, err := h.Nodes.Get(nodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	configBytes, err := h.generateConfig(nodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.pushViaSSH(node, configBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "push failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "pushed", "node": node.Name})
}

func (h *ConfigHandler) getRawConfig(w http.ResponseWriter, r *http.Request) {
	nodeID := parseNodeIDFromConfigPath(r.URL.Path)
	if nodeID == 0 {
		writeError(w, http.StatusBadRequest, "invalid node id")
		return
	}
	node, err := h.Nodes.Get(nodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	client, err := h.sshConnect(node)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ssh connect: "+err.Error())
		return
	}
	defer client.Close()

	out, err := sshRun(client, "cat "+node.ConfigPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read config: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(out))
}

func (h *ConfigHandler) putRawConfig(w http.ResponseWriter, r *http.Request) {
	nodeID := parseNodeIDFromConfigPath(r.URL.Path)
	if nodeID == 0 {
		writeError(w, http.StatusBadRequest, "invalid node id")
		return
	}
	node, err := h.Nodes.Get(nodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body failed")
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "empty body")
		return
	}

	client, err := h.sshConnect(node)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ssh connect: "+err.Error())
		return
	}
	defer client.Close()

	tmpPath := "/tmp/singbox-panel-raw-config.json"
	if err := sshWriteFile(client, tmpPath, body); err != nil {
		writeError(w, http.StatusInternalServerError, "write temp: "+err.Error())
		return
	}

	if node.ProxyType == "singbox" {
		checkCmd := fmt.Sprintf("%s check -c %s", node.SingboxBin, tmpPath)
		if out, err := sshRun(client, checkCmd); err != nil {
			sshRun(client, "rm -f "+tmpPath)
			writeError(w, http.StatusBadRequest, "config validation failed: "+strings.TrimSpace(out))
			return
		}
	}

	mvCmd := fmt.Sprintf("mkdir -p $(dirname %s) && cp %s %s && rm %s", node.ConfigPath, tmpPath, node.ConfigPath, tmpPath)
	if out, err := sshRun(client, mvCmd); err != nil {
		writeError(w, http.StatusInternalServerError, "move config: "+out)
		return
	}

	var restart bool
	if r.URL.Query().Get("restart") == "true" {
		svcName := "sing-box"
		if node.ProxyType == "xray" {
			svcName = "xray"
		}
		if out, err := sshRun(client, "systemctl restart "+svcName); err != nil {
			writeError(w, http.StatusInternalServerError, "restart failed: "+out)
			return
		}
		restart = true
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "node": node.Name, "restarted": restart})
}

func (h *ConfigHandler) generateConfig(nodeID int) ([]byte, error) {
	allowedUserIDs, err := h.Access.UsersForNode(nodeID)
	if err != nil {
		return nil, err
	}
	allUsers, err := h.Users.ListEnabled()
	if err != nil {
		return nil, err
	}
	// Filter to only users with access to this node
	allowed := make(map[int]bool, len(allowedUserIDs))
	for _, id := range allowedUserIDs {
		allowed[id] = true
	}
	var users []model.User
	for _, u := range allUsers {
		if allowed[u.ID] {
			users = append(users, u)
		}
	}
	inbounds, err := h.Nodes.ListInbounds(nodeID)
	if err != nil {
		return nil, err
	}
	return singbox.GenerateConfig(users, inbounds)
}

// SyncNodes regenerates and pushes configuration only to the requested nodes.
// Results are returned per node so callers can distinguish a saved database
// change from a failed remote synchronization.
func (h *ConfigHandler) SyncNodes(nodeIDs []int) []NodeSyncResult {
	nodeIDs = uniqueNodeIDs(nodeIDs)
	results := make([]NodeSyncResult, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		result := NodeSyncResult{NodeID: nodeID}
		node, err := h.Nodes.Get(nodeID)
		if err != nil {
			result.Status = "error"
			result.Error = "node not found"
			results = append(results, result)
			continue
		}
		result.Node = node.Name
		if !node.Enabled {
			result.Status = "skipped"
			result.Error = "node disabled"
			results = append(results, result)
			continue
		}
		if node.ProxyType != "singbox" {
			result.Status = "skipped"
			result.Error = "not singbox"
			results = append(results, result)
			continue
		}
		configBytes, err := h.generateConfig(nodeID)
		if err != nil {
			result.Status = "error"
			result.Error = "generate: " + err.Error()
			results = append(results, result)
			continue
		}
		if err := h.pushViaSSH(node, configBytes); err != nil {
			result.Status = "error"
			result.Error = "push: " + err.Error()
			results = append(results, result)
			continue
		}
		result.Status = "pushed"
		results = append(results, result)
	}
	return results
}

func uniqueNodeIDs(ids []int) []int {
	seen := make(map[int]struct{}, len(ids))
	result := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Ints(result)
	return result
}

// Traffic reporting

type TrafficReportReq struct {
	NodeID int                 `json:"node_id"`
	Users  map[string][2]int64 `json:"users"`
	TS     int64               `json:"timestamp"`
}

func (h *ConfigHandler) HandleTrafficReport(w http.ResponseWriter, r *http.Request) {
	nodeToken := r.Header.Get("X-Node-Token")

	var req TrafficReportReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	node, err := h.Nodes.GetByToken(nodeToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid node token")
		return
	}

	users, err := h.Users.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	uuidToUser := make(map[string]*model.User, len(users))
	nameToUser := make(map[string]*model.User, len(users))
	for i := range users {
		uuidToUser[users[i].UUID] = &users[i]
		nameToUser[users[i].Name] = &users[i]
	}

	for key, traffic := range req.Users {
		user := nameToUser[key]
		if user == nil {
			user = uuidToUser[key]
		}
		if user == nil {
			continue
		}
		up, down := traffic[0], traffic[1]
		if up == 0 && down == 0 {
			continue
		}
		h.Users.AddTraffic(user.ID, up, down)
		h.Nodes.RecordTraffic(node.ID, user.ID, up, down)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Stats endpoints

func (h *ConfigHandler) HandleUserStats(w http.ResponseWriter, r *http.Request) {
	users, err := h.Users.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type UserStat struct {
		ID         int    `json:"id"`
		Name       string `json:"name"`
		Enabled    bool   `json:"enabled"`
		UsedBytes  int64  `json:"used_bytes"`
		UpBytes    int64  `json:"up_bytes"`
		DownBytes  int64  `json:"down_bytes"`
		LimitBytes int64  `json:"limit_bytes"`
		ExpireAt   string `json:"expire_at"`
	}
	stats := make([]UserStat, 0, len(users))
	for _, u := range users {
		stats = append(stats, UserStat{
			ID: u.ID, Name: u.Name, Enabled: u.Enabled,
			UsedBytes: u.TrafficUsedBytes, UpBytes: u.TrafficUpBytes, DownBytes: u.TrafficDownBytes,
			LimitBytes: u.TrafficLimitBytes, ExpireAt: u.ExpireAt,
		})
	}
	writeJSON(w, http.StatusOK, stats)
}

func (h *ConfigHandler) HandleNodeStats(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.Nodes.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type NodeStat struct {
		ID        int    `json:"id"`
		Name      string `json:"name"`
		Enabled   bool   `json:"enabled"`
		UpBytes   int64  `json:"up_bytes"`
		DownBytes int64  `json:"down_bytes"`
	}
	stats := make([]NodeStat, 0, len(nodes))
	for _, n := range nodes {
		up, down := h.Nodes.GetTrafficSum(n.ID)
		stats = append(stats, NodeStat{
			ID: n.ID, Name: n.Name, Enabled: n.Enabled,
			UpBytes: up, DownBytes: down,
		})
	}
	writeJSON(w, http.StatusOK, stats)
}

func (h *ConfigHandler) HandleTrafficHistory(w http.ResponseWriter, r *http.Request) {
	days := 7
	if d := r.URL.Query().Get("days"); d != "" {
		fmt.Sscanf(d, "%d", &days)
	}
	if days > 90 {
		days = 90
	}

	userID := r.URL.Query().Get("user_id")
	nodeID := r.URL.Query().Get("node_id")

	query := `SELECT date(recorded_at) as day, SUM(bytes_up) as up, SUM(bytes_down) as down FROM traffic_logs WHERE recorded_at >= date('now', ?)`
	args := []any{fmt.Sprintf("-%d days", days)}

	if userID != "" {
		query += " AND user_id = ?"
		args = append(args, userID)
	}
	if nodeID != "" {
		query += " AND node_id = ?"
		args = append(args, nodeID)
	}
	query += " GROUP BY day ORDER BY day"

	rows, err := h.Nodes.DB.Query(query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	type DayTraffic struct {
		Day  string `json:"day"`
		Up   int64  `json:"up"`
		Down int64  `json:"down"`
	}
	var result []DayTraffic
	for rows.Next() {
		var d DayTraffic
		rows.Scan(&d.Day, &d.Up, &d.Down)
		result = append(result, d)
	}
	if result == nil {
		result = []DayTraffic{}
	}
	writeJSON(w, http.StatusOK, result)
}

// SSH helpers

func (h *ConfigHandler) sshConnect(node *model.Node) (*ssh.Client, error) {
	var authMethods []ssh.AuthMethod

	// Try key auth first
	keyBytes, err := os.ReadFile(h.SSHKeyPath)
	if err == nil {
		signer, err := ssh.ParsePrivateKey(keyBytes)
		if err == nil {
			authMethods = append(authMethods, ssh.PublicKeys(signer))
		}
	}

	// Fallback to password if available
	if node.SSHPassword != "" {
		authMethods = append(authMethods, ssh.Password(node.SSHPassword))
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no auth method available (no key and no password)")
	}

	config := &ssh.ClientConfig{
		User:            node.SSHUser,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	// Handle IPv6 addresses properly
	host := node.Host
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	addr := fmt.Sprintf("%s:%d", host, node.Port)
	return ssh.Dial("tcp", addr, config)
}

func (h *ConfigHandler) pushViaSSH(node *model.Node, configBytes []byte) error {
	nodeLock := h.pushLock(node.ID)
	nodeLock.Lock()
	defer nodeLock.Unlock()

	client, err := h.sshConnect(node)
	if err != nil {
		return err
	}
	defer client.Close()

	tmpPath := "/tmp/singbox-panel-config.json"
	if err := sshWriteFile(client, tmpPath, configBytes); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}

	checkCmd := fmt.Sprintf("%s check -c %s", node.SingboxBin, tmpPath)
	if out, err := sshRun(client, checkCmd); err != nil {
		return fmt.Errorf("config check failed: %s: %w", out, err)
	}

	mvCmd := fmt.Sprintf("mkdir -p $(dirname %s) && cp %s %s && rm %s", node.ConfigPath, tmpPath, node.ConfigPath, tmpPath)
	if out, err := sshRun(client, mvCmd); err != nil {
		return fmt.Errorf("move config: %s: %w", out, err)
	}

	if out, err := sshRun(client, "systemctl restart sing-box"); err != nil {
		return fmt.Errorf("restart sing-box: %s: %w", out, err)
	}
	return nil
}

func (h *ConfigHandler) pushLock(nodeID int) *sync.Mutex {
	h.pushLocksMu.Lock()
	defer h.pushLocksMu.Unlock()
	if h.pushLocks == nil {
		h.pushLocks = make(map[int]*sync.Mutex)
	}
	lock := h.pushLocks[nodeID]
	if lock == nil {
		lock = &sync.Mutex{}
		h.pushLocks[nodeID] = lock
	}
	return lock
}

func sshRun(client *ssh.Client, cmd string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	out, err := session.CombinedOutput(cmd)
	return string(out), err
}

func sshWriteFile(client *ssh.Client, path string, data []byte) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdin = strings.NewReader(string(data))
	return session.Run("cat > " + path)
}

func parseNodeIDFromConfigPath(path string) int {
	path = strings.TrimPrefix(path, "/api/nodes/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return 0
	}
	return parseID("/api/nodes/"+parts[0], "/api/nodes/")
}

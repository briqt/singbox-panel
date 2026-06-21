package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/briqt/singbox-panel/model"
	"github.com/briqt/singbox-panel/singbox"
	"golang.org/x/crypto/ssh"
)

type ConfigHandler struct {
	Users      *model.UserStore
	Nodes      *model.NodeStore
	Access     *model.AccessStore
	SSHKeyPath string
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

	mvCmd := fmt.Sprintf("cp %s %s && rm %s", tmpPath, node.ConfigPath, tmpPath)
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

// Traffic reporting

type TrafficReportReq struct {
	NodeID int              `json:"node_id"`
	Users  map[string][2]int64 `json:"users"`
	TS     int64            `json:"timestamp"`
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
		h.Users.AddTraffic(user.ID, up+down)
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
		ID        int    `json:"id"`
		Name      string `json:"name"`
		Enabled   bool   `json:"enabled"`
		UsedBytes int64  `json:"used_bytes"`
		LimitBytes int64 `json:"limit_bytes"`
		ExpireAt  string `json:"expire_at"`
	}
	stats := make([]UserStat, 0, len(users))
	for _, u := range users {
		stats = append(stats, UserStat{
			ID: u.ID, Name: u.Name, Enabled: u.Enabled,
			UsedBytes: u.TrafficUsedBytes, LimitBytes: u.TrafficLimitBytes,
			ExpireAt: u.ExpireAt,
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
		ID      int    `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
		UpBytes   int64 `json:"up_bytes"`
		DownBytes int64 `json:"down_bytes"`
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

// SSH helpers

func (h *ConfigHandler) sshConnect(node *model.Node) (*ssh.Client, error) {
	keyBytes, err := os.ReadFile(h.SSHKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read ssh key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("parse ssh key: %w", err)
	}
	config := &ssh.ClientConfig{
		User:            node.SSHUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	addr := fmt.Sprintf("%s:%d", node.Host, node.Port)
	return ssh.Dial("tcp", addr, config)
}

func (h *ConfigHandler) pushViaSSH(node *model.Node, configBytes []byte) error {
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

	mvCmd := fmt.Sprintf("cp %s %s && rm %s", tmpPath, node.ConfigPath, tmpPath)
	if out, err := sshRun(client, mvCmd); err != nil {
		return fmt.Errorf("move config: %s: %w", out, err)
	}

	if out, err := sshRun(client, "systemctl restart sing-box"); err != nil {
		return fmt.Errorf("restart sing-box: %s: %w", out, err)
	}
	return nil
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

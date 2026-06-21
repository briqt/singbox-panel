package handler

import (
	"encoding/json"
	"fmt"
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
	SSHKeyPath string
}

func (h *ConfigHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/generate"):
		h.generate(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/push"):
		h.push(w, r)
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

func (h *ConfigHandler) generateConfig(nodeID int) ([]byte, error) {
	users, err := h.Users.ListEnabled()
	if err != nil {
		return nil, err
	}
	inbounds, err := h.Nodes.ListInbounds(nodeID)
	if err != nil {
		return nil, err
	}
	return singbox.GenerateConfig(users, inbounds)
}

func (h *ConfigHandler) pushViaSSH(node *model.Node, configBytes []byte) error {
	keyBytes, err := os.ReadFile(h.SSHKeyPath)
	if err != nil {
		return fmt.Errorf("read ssh key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return fmt.Errorf("parse ssh key: %w", err)
	}

	config := &ssh.ClientConfig{
		User:            node.SSHUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	addr := fmt.Sprintf("%s:%d", node.Host, node.Port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return fmt.Errorf("ssh dial: %w", err)
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

	restartCmd := "systemctl restart sing-box"
	if out, err := sshRun(client, restartCmd); err != nil {
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

	escaped := strings.ReplaceAll(string(data), "'", "'\"'\"'")
	cmd := fmt.Sprintf("cat > %s << 'SINGBOX_EOF'\n%s\nSINGBOX_EOF", path, escaped)
	_, err = session.CombinedOutput(cmd)
	return err
}

func parseNodeIDFromConfigPath(path string) int {
	path = strings.TrimPrefix(path, "/api/nodes/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return 0
	}
	return parseID("/api/nodes/"+parts[0], "/api/nodes/")
}

type TrafficReportReq struct {
	NodeID int                    `json:"node_id"`
	Users  map[string][2]int64    `json:"users"`
	TS     int64                  `json:"timestamp"`
}

func (h *ConfigHandler) HandleTrafficReport(w http.ResponseWriter, r *http.Request) {
	var req TrafficReportReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

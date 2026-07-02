package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/briqt/singbox-panel/model"
	"golang.org/x/crypto/ssh"
)

type NodeOpsHandler struct {
	Nodes  *model.NodeStore
	Config *ConfigHandler
}

func (h *NodeOpsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/setup-ssh"):
		h.setupSSH(w, r)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/version"):
		h.getVersion(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/install"):
		h.install(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/upgrade"):
		h.install(w, r)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/status"):
		h.getStatus(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *NodeOpsHandler) getVersion(w http.ResponseWriter, r *http.Request) {
	nodeID := parseNodeIDFromConfigPath(r.URL.Path)
	node, err := h.Nodes.Get(nodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	client, err := h.Config.sshConnect(node)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ssh: "+err.Error())
		return
	}
	defer client.Close()

	out, err := sshRun(client, node.SingboxBin+" version 2>/dev/null | head -1")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"installed": false, "node": node.Name})
		return
	}
	version := strings.TrimSpace(out)
	writeJSON(w, http.StatusOK, map[string]any{"installed": true, "version": version, "node": node.Name, "bin": node.SingboxBin})
}

func (h *NodeOpsHandler) getStatus(w http.ResponseWriter, r *http.Request) {
	nodeID := parseNodeIDFromConfigPath(r.URL.Path)
	node, err := h.Nodes.Get(nodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}

	client, err := h.Config.sshConnect(node)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"node": node.Name, "reachable": false, "error": err.Error()})
		return
	}
	defer client.Close()

	// Check version
	verOut, _ := sshRun(client, node.SingboxBin+" version 2>/dev/null | head -1")
	installed := strings.TrimSpace(verOut) != ""

	// Check service status
	svcOut, _ := sshRun(client, "systemctl is-active sing-box 2>/dev/null")
	running := strings.TrimSpace(svcOut) == "active"
	socketOut, socketErr := sshRun(client, "ss -H -lntu 2>/dev/null")
	listeningSockets := parseListeningSockets(socketOut)
	inbounds, err := h.Nodes.ListInbounds(node.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	inboundStatuses := make([]map[string]any, 0, len(inbounds))
	for _, inbound := range inbounds {
		network := "tcp"
		if inbound.Protocol == "hysteria2" {
			network = "udp"
		}
		var listening any
		if socketErr == nil {
			listening = listeningSockets[network][inbound.Port]
		}
		inboundStatuses = append(inboundStatuses, map[string]any{
			"id": inbound.ID, "protocol": inbound.Protocol, "port": inbound.Port, "network": network,
			"listening": listening,
		})
	}

	// System info: memory + disk
	sysOut, _ := sshRun(client, `printf '{"mem_total":%s,"mem_available":%s,"disk_total":%s,"disk_used":%s,"uptime":%s}' \
		$(awk '/MemTotal/{t=$2} /MemAvailable/{a=$2} END{printf "%d %d",t*1024,a*1024}' /proc/meminfo) \
		$(df / --output=size,used -B1 | tail -1 | awk '{printf "%s %s",$1,$2}') \
		$(awk '{printf "%d",$1}' /proc/uptime)`)

	var sysInfo map[string]int64
	json.Unmarshal([]byte(sysOut), &sysInfo)

	result := map[string]any{
		"node":      node.Name,
		"reachable": true,
		"installed": installed,
		"version":   strings.TrimSpace(verOut),
		"running":   running,
		"inbounds":  inboundStatuses,
	}
	if socketErr != nil {
		result["listener_check_error"] = "ss command unavailable or failed"
	}
	if sysInfo != nil {
		result["mem_total"] = sysInfo["mem_total"]
		result["mem_available"] = sysInfo["mem_available"]
		result["disk_total"] = sysInfo["disk_total"]
		result["disk_used"] = sysInfo["disk_used"]
		result["uptime"] = sysInfo["uptime"]
	}
	writeJSON(w, http.StatusOK, result)
}

var socketPortPattern = regexp.MustCompile(`:([0-9]+)$`)

func parseListeningSockets(output string) map[string]map[int]bool {
	result := map[string]map[int]bool{"tcp": {}, "udp": {}}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		network := strings.ToLower(fields[0])
		if strings.HasPrefix(network, "tcp") {
			network = "tcp"
		} else if strings.HasPrefix(network, "udp") {
			network = "udp"
		} else {
			continue
		}
		for _, field := range fields[1:] {
			match := socketPortPattern.FindStringSubmatch(field)
			if len(match) != 2 {
				continue
			}
			port, err := strconv.Atoi(match[1])
			if err == nil && port > 0 {
				result[network][port] = true
				break
			}
		}
	}
	return result
}

type InstallReq struct {
	Version string `json:"version"`
}

func (h *NodeOpsHandler) install(w http.ResponseWriter, r *http.Request) {
	nodeID := parseNodeIDFromConfigPath(r.URL.Path)
	node, err := h.Nodes.Get(nodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}

	var req InstallReq
	json.NewDecoder(r.Body).Decode(&req)
	version := req.Version
	if version == "" {
		version = "latest"
	}

	client, err := h.Config.sshConnect(node)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ssh: "+err.Error())
		return
	}
	defer client.Close()

	arch := "amd64"
	archOut, _ := sshRun(client, "uname -m")
	if strings.Contains(archOut, "aarch64") || strings.Contains(archOut, "arm64") {
		arch = "arm64"
	}

	// Resolve latest version if needed
	if version == "latest" {
		resolveCmd := `curl -sL "https://api.github.com/repos/SagerNet/sing-box/releases/latest" | grep -o '"tag_name":"[^"]*"' | head -1 | cut -d'"' -f4`
		tagOut, err := sshRun(client, resolveCmd)
		if err != nil || strings.TrimSpace(tagOut) == "" {
			writeError(w, http.StatusInternalServerError, "failed to resolve latest version")
			return
		}
		version = strings.TrimPrefix(strings.TrimSpace(tagOut), "v")
	} else {
		version = strings.TrimPrefix(version, "v")
	}

	binPath := node.SingboxBin
	if binPath == "" {
		binPath = "/usr/local/bin/sing-box"
	}

	// Download and install
	url := fmt.Sprintf("https://github.com/SagerNet/sing-box/releases/download/v%s/sing-box-%s-linux-%s.tar.gz", version, version, arch)
	installScript := fmt.Sprintf(`
set -e
cd /tmp
rm -rf sing-box-install
mkdir sing-box-install && cd sing-box-install
curl -sL "%s" -o sb.tar.gz
tar xzf sb.tar.gz
find . -name "sing-box" -type f -exec cp {} %s \;
chmod +x %s
rm -rf /tmp/sing-box-install
%s version
`, url, binPath, binPath, binPath)

	out, err := sshRun(client, installScript)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "install failed: "+strings.TrimSpace(out))
		return
	}

	// Ensure systemd service exists
	svcCheck, _ := sshRun(client, "test -f /etc/systemd/system/sing-box.service && echo exists")
	if !strings.Contains(svcCheck, "exists") {
		svcContent := fmt.Sprintf(`[Unit]
Description=Sing-Box Service
After=network.target

[Service]
Type=simple
ExecStart=%s run -c %s
Restart=always
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`, binPath, node.ConfigPath)
		sshWriteFile(client, "/etc/systemd/system/sing-box.service", []byte(svcContent))
		sshRun(client, "systemctl daemon-reload && systemctl enable sing-box")
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "installed",
		"node":    node.Name,
		"version": "v" + version,
		"bin":     binPath,
		"output":  strings.TrimSpace(out),
	})
}

type SetupSSHReq struct {
	Password string `json:"password"`
}

func (h *NodeOpsHandler) setupSSH(w http.ResponseWriter, r *http.Request) {
	nodeID := parseNodeIDFromConfigPath(r.URL.Path)
	node, err := h.Nodes.Get(nodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}

	var req SetupSSHReq
	json.NewDecoder(r.Body).Decode(&req)

	// Read panel's public key
	pubKeyBytes, err := os.ReadFile(h.Config.SSHKeyPath + ".pub")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot read panel public key: "+err.Error())
		return
	}
	pubKey := strings.TrimSpace(string(pubKeyBytes))

	// Connect with password
	password := req.Password
	if password == "" {
		password = node.SSHPassword
	}
	if password == "" {
		writeError(w, http.StatusBadRequest, "password required (provide in request body or set ssh_password on node)")
		return
	}

	sshConfig := &ssh.ClientConfig{
		User:            node.SSHUser,
		Auth:            []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	host := node.Host
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	addr := fmt.Sprintf("%s:%d", host, node.Port)
	client, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ssh connect with password failed: "+err.Error())
		return
	}
	defer client.Close()

	// Inject public key
	injectCmd := fmt.Sprintf(`mkdir -p ~/.ssh && chmod 700 ~/.ssh && grep -qF "%s" ~/.ssh/authorized_keys 2>/dev/null || echo "%s" >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && echo "key_added"`, pubKey, pubKey)
	out, err := sshRun(client, injectCmd)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "inject key failed: "+strings.TrimSpace(out))
		return
	}

	// Save password to node for future fallback
	if req.Password != "" {
		h.Nodes.DB.Exec(`UPDATE nodes SET ssh_password = ? WHERE id = ?`, req.Password, node.ID)
	}

	// Verify key auth works
	verifyClient, err := h.Config.sshConnect(node)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "key_injected_but_verify_failed", "node": node.Name, "error": err.Error()})
		return
	}
	verifyClient.Close()

	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "node": node.Name, "message": "SSH key auth configured successfully"})
}

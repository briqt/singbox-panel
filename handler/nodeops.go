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

// singboxRepo is the GitHub repo the panel installs sing-box from. It must be a
// build that includes the with_v2ray_api tag (see custom-build workflow),
// because the panel's generated config enables the v2ray_api StatsService for
// per-user traffic accounting. Upstream SagerNet releases omit that tag.
const singboxRepo = "briqt/sing-box"

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

	// Resolve latest version if needed. Follow the releases/latest redirect
	// (…/releases/tag/vX.Y.Z) instead of the JSON API — the API is rate-limited
	// for unauthenticated callers (60/hr per IP) and fails intermittently from
	// nodes, whereas the redirect endpoint has no such limit.
	if version == "latest" {
		resolveCmd := `curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/` + singboxRepo + `/releases/latest"`
		urlOut, err := sshRun(client, resolveCmd)
		tag := strings.TrimSpace(urlOut)
		if i := strings.LastIndex(tag, "/tag/"); i >= 0 {
			tag = tag[i+len("/tag/"):]
		} else {
			tag = ""
		}
		if err != nil || tag == "" {
			writeError(w, http.StatusInternalServerError, "failed to resolve latest version from "+singboxRepo)
			return
		}
		version = strings.TrimPrefix(tag, "v")
	} else {
		version = strings.TrimPrefix(version, "v")
	}

	binPath := node.SingboxBin
	if binPath == "" {
		binPath = "/usr/local/bin/sing-box"
	}

	// Download and install. Uses the fork build (with_v2ray_api tag) so the
	// installed binary can run the per-user stats config the panel generates;
	// upstream release binaries omit v2ray_api and reject that config.
	url := fmt.Sprintf("https://github.com/%s/releases/download/v%s/sing-box-%s-linux-%s.tar.gz", singboxRepo, version, version, arch)
	// Install with an atomic rename: cp-over-in-place fails with ETXTBSY
	// ("text file busy") when sing-box is already running, so we stage the new
	// binary beside the target and mv it into place. rename(2) swaps the
	// directory entry while the running process keeps its old inode, so the
	// upgrade takes effect on the next restart (the config push that follows).
	installScript := fmt.Sprintf(`
set -e
cd /tmp
rm -rf sing-box-install
mkdir sing-box-install && cd sing-box-install
curl -sL "%s" -o sb.tar.gz
tar xzf sb.tar.gz
BIN=$(find . -name "sing-box" -type f | head -1)
[ -n "$BIN" ] || { echo "sing-box binary not found in archive"; exit 1; }
install -m 0755 "$BIN" "%s.new"
mv -f "%s.new" "%s"
rm -rf /tmp/sing-box-install
%s version
`, url, binPath, binPath, binPath, binPath)

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

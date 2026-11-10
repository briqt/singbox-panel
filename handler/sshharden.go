package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/briqt/singbox-panel/model"
)

// Public-key-only SSH on nodes.
//
// The panel already reaches every node with its own key. Password login is then
// pure exposure: root on the public internet, guessable, and — for nodes added
// through setup-ssh — a password that has passed through chat logs. Hardening
// turns it off, but only after proving the key works, and verifies the result
// by asking the *running* sshd which methods it still offers, not by reading
// the file it wrote.

// sshHardenDropIn sorts ahead of distro and cloud-init drop-ins ("00-hardening",
// "50-cloud-init"): sshd keeps the first value it reads for most keywords, and
// the Include sits near the top of sshd_config on Debian and Ubuntu.
const sshHardenDropIn = "/etc/ssh/sshd_config.d/00-00-singbox-panel.conf"

const sshHardenContent = `# Managed by singbox-panel: public-key authentication only.
# Remove this file and reload ssh to re-enable passwords.
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin prohibit-password
`

// wantSSHD is the effective configuration a hardened node must report.
// sshd -T prints prohibit-password under its legacy name.
var wantSSHD = map[string][]string{
	"passwordauthentication":       {"no"},
	"kbdinteractiveauthentication": {"no"},
	"permitrootlogin":              {"without-password", "prohibit-password"},
}

// sshConnectKeyOnly dials with the panel key and nothing else, so success
// proves key authentication on its own, independent of any stored password.
func (h *ConfigHandler) sshConnectKeyOnly(node *model.Node) (*ssh.Client, error) {
	keyBytes, err := os.ReadFile(h.SSHKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read panel key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("parse panel key: %w", err)
	}
	return ssh.Dial("tcp", sshDialAddr(node), &ssh.ClientConfig{
		User:            node.SSHUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: h.hostKeyCallback(),
		Timeout:         10 * time.Second,
	})
}

// OfferedAuth records which interactive methods a live server offered. The
// callbacks refuse to answer, so no password or response ever leaves the panel.
type OfferedAuth struct {
	Password            bool `json:"password"`
	KeyboardInteractive bool `json:"keyboard_interactive"`
}

var errProbeOnly = errors.New("probe only: no credential is sent")

// probeOfferedAuth opens a fresh connection that offers no key and declines
// every prompt. Whatever callback fires is a method the server still accepts.
func (h *ConfigHandler) probeOfferedAuth(node *model.Node) (OfferedAuth, error) {
	var offered OfferedAuth
	client, err := ssh.Dial("tcp", sshDialAddr(node), &ssh.ClientConfig{
		User: node.SSHUser,
		Auth: []ssh.AuthMethod{
			ssh.PasswordCallback(func() (string, error) {
				offered.Password = true
				return "", errProbeOnly
			}),
			ssh.KeyboardInteractive(func(string, string, []string, []bool) ([]string, error) {
				offered.KeyboardInteractive = true
				return nil, errProbeOnly
			}),
		},
		HostKeyCallback: h.hostKeyCallback(),
		Timeout:         10 * time.Second,
	})
	if err == nil {
		client.Close()
		return offered, errors.New("server accepted a connection without credentials")
	}
	var mismatch *ErrHostKeyMismatch
	if errors.As(err, &mismatch) {
		return offered, err
	}
	// The expected outcome is an authentication failure — either the server ran
	// out of methods, or one of our declining callbacks aborted the handshake
	// (x/crypto returns the callback's error as-is). Anything that never reached
	// authentication (refused, timeout) is not evidence either way.
	reached := errors.Is(err, errProbeOnly) ||
		strings.Contains(err.Error(), errProbeOnly.Error()) ||
		strings.Contains(err.Error(), "unable to authenticate")
	if !reached {
		return offered, fmt.Errorf("probe did not reach authentication: %w", err)
	}
	return offered, nil
}

// SSHHardenResult is the read-back of a hardening run.
type SSHHardenResult struct {
	Node        string            `json:"node"`
	Status      string            `json:"status"` // hardened | already | failed
	Effective   map[string]string `json:"effective"`
	Ineffective map[string]string `json:"ineffective,omitempty"`
	Offered     OfferedAuth       `json:"offered_after"`
	Sources     string            `json:"sources,omitempty"` // where the conflicting values come from
	Error       string            `json:"error,omitempty"`
}

func parseSSHDT(out string) map[string]string {
	got := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			if _, ok := wantSSHD[strings.ToLower(fields[0])]; ok {
				got[strings.ToLower(fields[0])] = strings.ToLower(fields[1])
			}
		}
	}
	return got
}

// ineffectiveSSHD returns keys whose effective value is not what hardening
// requires (missing keys included).
func ineffectiveSSHD(got map[string]string) map[string]string {
	bad := map[string]string{}
	keys := make([]string, 0, len(wantSSHD))
	for k := range wantSSHD {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v, ok := got[k]
		if !ok {
			bad[k] = "missing"
			continue
		}
		accepted := false
		for _, w := range wantSSHD[k] {
			accepted = accepted || v == w
		}
		if !accepted {
			bad[k] = v
		}
	}
	return bad
}

// The effective value is evaluated for root from an arbitrary public address,
// so a Match block that re-enables passwords for some source shows up.
const sshdEffectiveCmd = `sshd -T -C user=root,host=panel-check,addr=203.0.113.7 2>&1`

func (h *NodeOpsHandler) HardenSSH(node *model.Node) (*SSHHardenResult, error) {
	res := &SSHHardenResult{Node: node.Name, Status: "failed"}
	// 1. The key must work on its own before passwords go away.
	client, err := h.Config.sshConnectKeyOnly(node)
	if err != nil {
		return res, fmt.Errorf("panel key authentication does not work; refusing to disable passwords: %w", err)
	}
	defer client.Close()

	before, _ := sshRun(client, sshdEffectiveCmd)
	if len(ineffectiveSSHD(parseSSHDT(before))) == 0 {
		if offered, err := h.Config.probeOfferedAuth(node); err == nil && !offered.Password && !offered.KeyboardInteractive {
			res.Status, res.Effective, res.Offered = "already", parseSSHDT(before), offered
			h.clearStoredPassword(node)
			return res, nil
		}
	}

	// 2. Write the drop-in, validate the whole configuration, then reload.
	if err := sshWriteFile(client, sshHardenDropIn, []byte(sshHardenContent)); err != nil {
		return res, fmt.Errorf("write %s: %w", sshHardenDropIn, err)
	}
	restore := func() {
		sshRun(client, "rm -f "+sshHardenDropIn+"; systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || true")
	}
	if out, err := sshRun(client, "chmod 0644 "+sshHardenDropIn+" && sshd -t 2>&1"); err != nil {
		restore()
		return res, fmt.Errorf("sshd -t rejected the configuration: %s", strings.TrimSpace(out))
	}
	// Ubuntu 24.04 socket-activates sshd per connection, so there may be no
	// daemon to reload; new connections read the new configuration anyway.
	if out, err := sshRun(client, `systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || systemctl is-active --quiet ssh.socket`); err != nil {
		restore()
		return res, fmt.Errorf("reload sshd: %s", strings.TrimSpace(out))
	}

	// 3. Read back the effective configuration…
	after, _ := sshRun(client, sshdEffectiveCmd)
	res.Effective = parseSSHDT(after)
	if bad := ineffectiveSSHD(res.Effective); len(bad) > 0 {
		res.Ineffective = bad
		res.Sources, _ = sshRun(client, `grep -RniE '^\s*(Include|Match|PasswordAuthentication|KbdInteractiveAuthentication|ChallengeResponseAuthentication|PermitRootLogin)\b' /etc/ssh/sshd_config /etc/ssh/sshd_config.d/ 2>/dev/null | head -40`)
		restore()
		return res, errors.New("hardening did not take effect; an earlier sshd_config line wins (see sources). Rolled back")
	}
	// …and what the live server actually offers to a new client.
	time.Sleep(time.Second)
	offered, err := h.Config.probeOfferedAuth(node)
	res.Offered = offered
	if err != nil {
		restore()
		return res, fmt.Errorf("could not verify offered methods, rolled back: %w", err)
	}
	if offered.Password || offered.KeyboardInteractive {
		restore()
		return res, errors.New("server still offers password or keyboard-interactive authentication after reload; rolled back")
	}
	// 4. A brand-new key-only login must still succeed, or we just locked
	// everyone out. The original connection is still open to undo it.
	verify, err := h.Config.sshConnectKeyOnly(node)
	if err != nil {
		restore()
		return res, fmt.Errorf("key login failed after hardening, rolled back: %w", err)
	}
	verify.Close()
	h.clearStoredPassword(node)
	res.Status = "hardened"
	return res, nil
}

func (h *NodeOpsHandler) clearStoredPassword(node *model.Node) {
	if node.SSHPassword != "" {
		h.Nodes.DB.Exec(`UPDATE nodes SET ssh_password = '' WHERE id = ?`, node.ID)
	}
}

func (h *NodeOpsHandler) sshHarden(w http.ResponseWriter, r *http.Request) {
	node, err := h.Nodes.Get(parseNodeIDFromConfigPath(r.URL.Path))
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	res, err := h.HardenSSH(node)
	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusBadGateway, res)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// HandleBatchSSHHarden hardens every enabled node, one at a time, and reports
// each. 200 all, 207 some, 502 none.
func (h *NodeOpsHandler) HandleBatchSSHHarden(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var req struct {
		NodeIDs []int `json:"node_ids"`
		DryRun  bool  `json:"dry_run"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
	}
	nodes, err := h.Nodes.ListEnabled()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	want := map[int]bool{}
	for _, id := range req.NodeIDs {
		want[id] = true
	}
	var results []*SSHHardenResult
	ok, total := 0, 0
	for i := range nodes {
		node := nodes[i]
		if len(want) > 0 && !want[node.ID] {
			continue
		}
		total++
		if req.DryRun {
			results = append(results, &SSHHardenResult{Node: node.Name, Status: "would-harden"})
			continue
		}
		res, err := h.HardenSSH(&node)
		if err != nil {
			res.Error = err.Error()
		} else {
			ok++
		}
		results = append(results, res)
	}
	code := http.StatusOK
	switch {
	case req.DryRun:
	case total > 0 && ok == 0:
		code = http.StatusBadGateway
	case ok < total:
		code = http.StatusMultiStatus
	}
	writeJSON(w, code, map[string]any{"results": results, "ok": ok, "total": total, "dry_run": req.DryRun})
}

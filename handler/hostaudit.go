package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/briqt/singbox-panel/model"
	"github.com/briqt/singbox-panel/nodeaudit"
)

// HostAuditHandler owns whole-machine traffic accounting: installing the
// node-side collector, reading its status, and serving the hourly conclusions
// the poller pulled into the panel database.
//
// Two numbers answer two different questions, and the panel keeps them apart:
// proxy usage (traffic_logs) is what sing-box carried for each user; host
// traffic is what the machine's NIC actually moved, which is what a provider
// bills — proxy traffic in *and* out, plus everything else on the box.
type HostAuditHandler struct {
	Nodes   *model.NodeStore
	Config  *ConfigHandler
	Store   *model.HostTrafficStore
	Traffic *model.TrafficStore
}

func (h *HostAuditHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	nodeID := parseNodeIDFromConfigPath(r.URL.Path)
	node, err := h.Nodes.Get(nodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	switch r.Method {
	case http.MethodPost:
		var req struct {
			Interface string `json:"interface"`
		}
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid json")
				return
			}
		}
		if req.Interface != "" && !validInterfaceName(req.Interface) {
			writeError(w, http.StatusBadRequest, "invalid interface name")
			return
		}
		result, err := h.Install(node, req.Interface)
		if err != nil {
			var pre *auditPreflightError
			if errors.As(err, &pre) {
				writeJSON(w, http.StatusPreconditionFailed, map[string]any{"error": err.Error(), "preflight": pre.preflight})
				return
			}
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "result": result})
			return
		}
		writeJSON(w, http.StatusOK, result)
	case http.MethodGet:
		client, err := h.Config.sshConnect(node)
		if err != nil {
			writeError(w, http.StatusBadGateway, "ssh: "+err.Error())
			return
		}
		defer client.Close()
		status, err := readAuditStatus(client)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"node": node.Name, "installed": false, "detail": err.Error()})
			return
		}
		healthy, reason := status.Healthy()
		sum, _ := sshRun(client, "sha256sum "+nodeaudit.ScriptPath+" 2>/dev/null | cut -d' ' -f1")
		writeJSON(w, http.StatusOK, map[string]any{
			"node": node.Name, "installed": true, "healthy": healthy, "reason": reason, "status": status,
			"up_to_date": strings.TrimSpace(sum) == nodeaudit.ScriptSHA256(),
		})
	case http.MethodDelete:
		client, err := h.Config.sshConnect(node)
		if err != nil {
			writeError(w, http.StatusBadGateway, "ssh: "+err.Error())
			return
		}
		defer client.Close()
		out, err := sshRun(client, nodeaudit.UninstallScript())
		if err != nil {
			writeError(w, http.StatusBadGateway, "uninstall failed: "+strings.TrimSpace(out))
			return
		}
		// Read back: the files must be gone and the table removed.
		left, _ := sshRun(client, "ls "+nodeaudit.ScriptPath+" "+nodeaudit.TimerPath+" 2>/dev/null; nft list table inet traffic_audit >/dev/null 2>&1 && echo table-present")
		if strings.TrimSpace(left) != "" {
			writeError(w, http.StatusBadGateway, "uninstall left remnants: "+strings.TrimSpace(left))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"node": node.Name, "status": "removed", "detail": json.RawMessage(lastJSONLine(out))})
	default:
		http.NotFound(w, r)
	}
}

type auditPreflightError struct {
	preflight nodeaudit.Preflight
	blockers  []string
}

func (e *auditPreflightError) Error() string {
	return "node is not ready for traffic accounting: " + strings.Join(e.blockers, "; ")
}

// AuditInstallResult is what an install reports after reading the node back.
type AuditInstallResult struct {
	Node                string              `json:"node"`
	Preflight           nodeaudit.Preflight `json:"preflight"`
	Warnings            []string            `json:"warnings,omitempty"`
	Status              *nodeaudit.Status   `json:"status,omitempty"`
	SingboxPIDUnchanged bool                `json:"singbox_pid_unchanged"`
	ScriptSHA256        string              `json:"script_sha256"`
}

// Install puts the collector on the node (or upgrades it in place, keeping its
// buffered data) and reads the result back. Success means a real sample was
// taken and the business service was not restarted — not that files were
// copied.
func (h *HostAuditHandler) Install(node *model.Node, iface string) (*AuditInstallResult, error) {
	result := &AuditInstallResult{Node: node.Name, ScriptSHA256: nodeaudit.ScriptSHA256()}
	inbounds, err := h.Nodes.ListInbounds(node.ID)
	if err != nil {
		return result, err
	}
	client, err := h.Config.sshConnect(node)
	if err != nil {
		return result, fmt.Errorf("ssh: %w", err)
	}
	defer client.Close()

	out, err := sshRun(client, nodeaudit.PreflightScript())
	if err != nil {
		return result, fmt.Errorf("preflight: %s", strings.TrimSpace(out))
	}
	if err := json.Unmarshal([]byte(lastJSONLine(out)), &result.Preflight); err != nil {
		return result, fmt.Errorf("preflight output unreadable: %w", err)
	}
	if result.Preflight.NFT != "yes" {
		msg, err := sshRun(client, nodeaudit.InstallNFTScript())
		if err != nil {
			return result, fmt.Errorf("install nftables: %s", strings.TrimSpace(msg))
		}
		result.Warnings = append(result.Warnings, "nftables CLI "+strings.TrimSpace(msg))
		out, err = sshRun(client, nodeaudit.PreflightScript())
		if err != nil || json.Unmarshal([]byte(lastJSONLine(out)), &result.Preflight) != nil {
			return result, fmt.Errorf("preflight after installing nftables failed: %s", strings.TrimSpace(out))
		}
	}
	blockers, warnings := result.Preflight.Problems()
	result.Warnings = append(result.Warnings, warnings...)
	if len(blockers) > 0 {
		return result, &auditPreflightError{preflight: result.Preflight, blockers: blockers}
	}

	pidBefore := singboxMainPID(client)
	if _, err := sshRun(client, "mkdir -p /usr/local/libexec"); err != nil {
		return result, fmt.Errorf("prepare directory: %w", err)
	}
	desired := nodeaudit.DesiredFor(node, inbounds, iface)
	for _, f := range []struct {
		path string
		data []byte
	}{
		{nodeaudit.ScriptPath + ".new", nodeaudit.Script},
		{nodeaudit.ServicePath + ".new", nodeaudit.ServiceUnit},
		{nodeaudit.TimerPath + ".new", nodeaudit.TimerUnit},
		{nodeaudit.ConfigPath + ".desired", desired.JSON()},
	} {
		if err := sshWriteFile(client, f.path, f.data); err != nil {
			return result, fmt.Errorf("write %s: %w", f.path, err)
		}
	}
	out, err = sshRun(client, nodeaudit.ActivateScript())
	if err != nil {
		return result, fmt.Errorf("activate: %s", strings.TrimSpace(out))
	}
	var status nodeaudit.Status
	if err := json.Unmarshal([]byte(lastJSONLine(out)), &status); err != nil {
		return result, fmt.Errorf("status output unreadable: %w", err)
	}
	result.Status = &status
	pidAfter := singboxMainPID(client)
	result.SingboxPIDUnchanged = pidBefore == pidAfter
	if !result.SingboxPIDUnchanged {
		return result, fmt.Errorf("sing-box MainPID changed during install (%s -> %s)", pidBefore, pidAfter)
	}
	if ok, reason := status.Healthy(); !ok {
		return result, fmt.Errorf("collector not healthy after install: %s", reason)
	}
	return result, nil
}

func singboxMainPID(client *ssh.Client) string {
	out, _ := sshRun(client, "systemctl show -p MainPID --value sing-box 2>/dev/null")
	return strings.TrimSpace(out)
}

func readAuditStatus(client *ssh.Client) (*nodeaudit.Status, error) {
	out, err := sshRun(client, "test -f "+nodeaudit.ScriptPath+" || { echo not-installed; exit 3; }; "+nodeaudit.Python+" status")
	if err != nil {
		return nil, errors.New(strings.TrimSpace(out))
	}
	var status nodeaudit.Status
	if err := json.Unmarshal([]byte(lastJSONLine(out)), &status); err != nil {
		return nil, fmt.Errorf("status output unreadable: %w", err)
	}
	return &status, nil
}

// lastJSONLine picks the final line that looks like a JSON object, so a stray
// warning printed by a remote tool does not break parsing.
func lastJSONLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "{") {
			return line
		}
	}
	return "{}"
}

func validInterfaceName(name string) bool {
	if len(name) == 0 || len(name) > 15 {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-' || c == ':') {
			return false
		}
	}
	return true
}

// NodeAuditBatchResult mirrors reprovision's per-node shape.
type NodeAuditBatchResult struct {
	NodeID int                 `json:"node_id"`
	Node   string              `json:"node"`
	Status string              `json:"status"`
	Error  string              `json:"error,omitempty"`
	Result *AuditInstallResult `json:"result,omitempty"`
}

// HandleBatch installs or upgrades the collector on every enabled node, one at
// a time. 200 means all succeeded, 207 some, 502 none — a caller reading only
// the status code must not mistake a half-done fleet for a done one.
func (h *HostAuditHandler) HandleBatch(w http.ResponseWriter, r *http.Request) {
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
	var targets []model.Node
	for _, n := range nodes {
		if n.ProxyType == "singbox" && (len(want) == 0 || want[n.ID]) {
			targets = append(targets, n)
		}
	}
	if req.DryRun {
		var names []map[string]any
		for _, n := range targets {
			names = append(names, map[string]any{"node_id": n.ID, "node": n.Name})
		}
		writeJSON(w, http.StatusOK, map[string]any{"dry_run": true, "nodes": names})
		return
	}
	results := make([]NodeAuditBatchResult, 0, len(targets))
	ok := 0
	for i := range targets {
		node := targets[i]
		res, err := h.Install(&node, "")
		item := NodeAuditBatchResult{NodeID: node.ID, Node: node.Name, Status: "ok", Result: res}
		if err != nil {
			item.Status, item.Error = "failed", err.Error()
		} else {
			ok++
		}
		results = append(results, item)
	}
	code := http.StatusOK
	switch {
	case len(targets) > 0 && ok == 0:
		code = http.StatusBadGateway
	case ok < len(targets):
		code = http.StatusMultiStatus
	}
	writeJSON(w, code, map[string]any{"results": results, "ok": ok, "total": len(targets)})
}

// HandleQuery serves GET /api/host-traffic?node_id=&hours= (or &from=&to= in
// Unix seconds). Without node_id it lists which nodes have data.
func (h *HostAuditHandler) HandleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	if q.Get("node_id") == "" {
		coverage, err := h.Store.Coverage()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		nodes, err := h.Nodes.List()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		var out []map[string]any
		for _, n := range nodes {
			out = append(out, map[string]any{"node_id": n.ID, "node": n.Name, "last_hour": coverage[n.ID]})
		}
		writeJSON(w, http.StatusOK, map[string]any{"nodes": out, "retention": h.retention()})
		return
	}
	nodeID, err := strconv.Atoi(q.Get("node_id"))
	if err != nil || nodeID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid node_id")
		return
	}
	now := time.Now().Unix()
	to := now/3600*3600 + 3600
	from := to - 24*3600
	if v := q.Get("hours"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 24*93 {
			writeError(w, http.StatusBadRequest, "hours must be 1..2232")
			return
		}
		from = to - int64(n)*3600
	}
	if v := q.Get("from"); v != "" {
		if from, err = strconv.ParseInt(v, 10, 64); err != nil {
			writeError(w, http.StatusBadRequest, "invalid from")
			return
		}
		from = from / 3600 * 3600
	}
	if v := q.Get("to"); v != "" {
		if to, err = strconv.ParseInt(v, 10, 64); err != nil {
			writeError(w, http.StatusBadRequest, "invalid to")
			return
		}
		to = (to + 3599) / 3600 * 3600
	}
	if to <= from {
		writeError(w, http.StatusBadRequest, "to must be after from")
		return
	}
	summary, err := h.Store.Summary(nodeID, from, to, peerCutoff(time.Now()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"summary": summary, "retention": h.retention()})
}

func (h *HostAuditHandler) retention() map[string]any {
	return map[string]any{
		"hourly_from":       h.hourCutoff(),
		"peers_days":        int(model.HostPeerRetention / (24 * time.Hour)),
		"node_buffer_hours": 48,
	}
}

func peerCutoff(now time.Time) int64 {
	return now.Add(-model.HostPeerRetention).Unix() / 3600 * 3600
}

// hourCutoff aligns the hourly conclusions with proxy usage retention, so the
// two views always cover the same calendar window.
func (h *HostAuditHandler) hourCutoff() int64 {
	if h.Traffic == nil {
		return time.Now().AddDate(0, -model.RetentionMonths, 0).Unix()
	}
	t, err := time.ParseInLocation("2006-01-02", h.Traffic.RetentionStart(), h.Traffic.Loc)
	if err != nil || h.Traffic.Loc == nil {
		return time.Now().AddDate(0, -model.RetentionMonths, 0).Unix()
	}
	return t.Unix()
}

// ---------------------------------------------------------------- pulling

const (
	hostPullInterval = 10 * time.Minute
	hostPullTimeout  = 90 * time.Second
)

// hostPuller pulls finished hours from every node's collector over the poller's
// pooled SSH connections. It runs apart from the per-minute user poll so a slow
// node-side aggregation can never delay usage accounting.
type hostPuller struct {
	audit *HostAuditHandler
	pool  *sshPool

	mu    sync.Mutex
	state map[int]string // last outcome per node, so logs fire on transitions only
}

func (p *hostPuller) loop(nodes func() ([]model.Node, error)) {
	time.Sleep(45 * time.Second)
	for {
		list, err := nodes()
		if err == nil {
			for i := range list {
				if list[i].ProxyType == "singbox" {
					p.pullGuarded(list[i])
				}
			}
		}
		time.Sleep(hostPullInterval)
	}
}

func (p *hostPuller) pullGuarded(node model.Node) {
	done := make(chan error, 1)
	go func() { done <- p.pull(node) }()
	var err error
	select {
	case err = <-done:
	case <-time.After(hostPullTimeout):
		p.pool.drop(node.ID)
		err = errors.New("pull timed out")
	}
	outcome := "ok"
	if errors.Is(err, errAuditNotInstalled) {
		outcome = "not-installed"
	} else if err != nil {
		outcome = "error: " + err.Error()
	}
	p.mu.Lock()
	prev := p.state[node.ID]
	p.state[node.ID] = outcome
	p.mu.Unlock()
	if prev != outcome && !(prev == "" && outcome == "ok") {
		log.Printf("host-traffic: %s %s", node.Name, outcome)
	}
}

var errAuditNotInstalled = errors.New("collector not installed")

func (p *hostPuller) pull(node model.Node) error {
	last, err := p.audit.Store.LastHour(node.ID)
	if err != nil {
		return err
	}
	since := int64(0)
	if last > 0 {
		since = last + 3600
	}
	client, err := p.pool.get(&node)
	if err != nil {
		return err
	}
	cmd := "test -f " + nodeaudit.ScriptPath + " || exit 3; nice -n 10 " + nodeaudit.Python + " hourly --since " + strconv.FormatInt(since, 10)
	session, err := client.NewSession()
	if err != nil {
		p.pool.drop(node.ID)
		return err
	}
	out, err := session.Output(cmd)
	session.Close()
	if err != nil {
		var exit *ssh.ExitError
		if errors.As(err, &exit) && exit.ExitStatus() == 3 {
			return errAuditNotInstalled
		}
		return fmt.Errorf("hourly: %w", err)
	}
	var report struct {
		Hours []model.HostTrafficHour `json:"hours"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		return fmt.Errorf("hourly output unreadable: %w", err)
	}
	return p.audit.Store.SaveHours(node.ID, report.Hours, peerCutoff(time.Now()))
}

func (p *hostPuller) prune() {
	if n, err := p.audit.Store.Prune(p.audit.hourCutoff(), peerCutoff(time.Now())); err == nil && n > 0 {
		log.Printf("host-traffic: pruned %d rows", n)
	}
}

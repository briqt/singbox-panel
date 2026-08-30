package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"

	"github.com/briqt/singbox-panel/model"
)

// ReprovisionHandler re-runs auto-setup across every enabled node.
//
// This exists because the alternative is what actually happened once: the
// protocol hardening of 2026-08-30 was rolled out by hand-typed ssh+curl loops
// that existed only in one terminal session. Nobody could re-run them, they
// carried an admin token to disk to avoid re-authenticating, and the sequence
// was not reviewable. Anything applied to every node is an operation the panel
// should own, with the same tests and release process as the rest of it.
//
// Re-provisioning is deliberately expressed in terms of the existing
// per-node endpoint rather than a second copy of its logic: auto-setup already
// owns credential preservation, port selection, cert issuance and rollback, and
// a parallel implementation would drift from it.
type ReprovisionHandler struct {
	Nodes *model.NodeStore
	Setup *SetupHandler
}

// NodeReprovisionResult is one node's outcome. Status is "ok" when the node's
// auto-setup returned 2xx, "failed" otherwise; Response carries auto-setup's
// own body verbatim so per-inbound detail is not flattened away.
type NodeReprovisionResult struct {
	NodeID   int             `json:"node_id"`
	Node     string          `json:"node"`
	Status   string          `json:"status"`
	HTTPCode int             `json:"http_code"`
	Response json.RawMessage `json:"response,omitempty"`
	Error    string          `json:"error,omitempty"`
}

type ReprovisionReq struct {
	// Mode is passed through to auto-setup ("auto", "direct", "cdn",
	// "reality"). Empty means "auto", which infers per node from DNS.
	Mode string `json:"mode"`
	// NodeIDs restricts the run. Empty means every enabled singbox node.
	NodeIDs []int `json:"node_ids"`
	// DryRun reports which nodes would be re-provisioned and stops. Rolling a
	// change across every node is the kind of operation worth previewing.
	DryRun bool `json:"dry_run"`
}

func (h *ReprovisionHandler) HandleReprovision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}

	var req ReprovisionReq
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
	}
	switch req.Mode {
	case "", "auto", "direct", "cdn", "reality":
	default:
		writeError(w, http.StatusBadRequest, "unsupported mode: "+req.Mode)
		return
	}

	nodes, err := h.Nodes.ListEnabled()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	targets := selectReprovisionTargets(nodes, req.NodeIDs)
	if len(targets) == 0 {
		writeError(w, http.StatusNotFound, "no enabled singbox node matched the request")
		return
	}

	if req.DryRun {
		planned := make([]map[string]any, 0, len(targets))
		for _, node := range targets {
			planned = append(planned, map[string]any{
				"node_id": node.ID, "node": node.Name, "domain": node.Domain,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"dry_run": true, "nodes": planned})
		return
	}

	// Sequential on purpose. Each node's auto-setup opens an SSH session, may
	// invoke acme.sh, and ends by pushing config and restarting sing-box. Doing
	// that to every node at once would take the whole fleet down together
	// instead of one node at a time, and would race the traffic poller's
	// connection pool.
	results := make([]NodeReprovisionResult, 0, len(targets))
	okCount := 0
	for _, node := range targets {
		result := h.reprovisionNode(node, req.Mode)
		if result.Status == "ok" {
			okCount++
		}
		results = append(results, result)
	}

	// 207 when the fleet is split: a caller that only checks the status code
	// must not read a partial rollout as a complete one.
	status := http.StatusOK
	if okCount == 0 {
		status = http.StatusBadGateway
	} else if okCount < len(results) {
		status = http.StatusMultiStatus
	}
	writeJSON(w, status, map[string]any{
		"nodes": results, "total": len(results), "succeeded": okCount,
		"failed": len(results) - okCount,
	})
}

// reprovisionNode drives the real auto-setup handler and captures its response,
// so there is exactly one implementation of what re-provisioning means.
func (h *ReprovisionHandler) reprovisionNode(node model.Node, mode string) NodeReprovisionResult {
	result := NodeReprovisionResult{NodeID: node.ID, Node: node.Name}

	body, err := json.Marshal(AutoSetupReq{Domain: node.Domain, Mode: mode})
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	path := "/api/nodes/" + strconv.Itoa(node.ID) + "/auto-setup"
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.Setup.HandleAutoSetup(rec, req)

	result.HTTPCode = rec.Code
	if raw := rec.Body.Bytes(); json.Valid(raw) {
		result.Response = append(json.RawMessage(nil), raw...)
	}
	if rec.Code >= 200 && rec.Code < 300 {
		result.Status = "ok"
	} else {
		result.Status = "failed"
		result.Error = fmt.Sprintf("auto-setup returned %d", rec.Code)
	}
	return result
}

// selectReprovisionTargets filters to singbox nodes, honouring an explicit
// node_ids list. Ordering follows the node list so a run is deterministic and
// its log can be read against the fleet.
func selectReprovisionTargets(nodes []model.Node, nodeIDs []int) []model.Node {
	wanted := make(map[int]bool, len(nodeIDs))
	for _, id := range nodeIDs {
		wanted[id] = true
	}
	targets := make([]model.Node, 0, len(nodes))
	for _, node := range nodes {
		if node.ProxyType != "singbox" {
			continue
		}
		if len(wanted) > 0 && !wanted[node.ID] {
			continue
		}
		targets = append(targets, node)
	}
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].SortOrder != targets[j].SortOrder {
			return targets[i].SortOrder < targets[j].SortOrder
		}
		return targets[i].ID < targets[j].ID
	})
	return targets
}

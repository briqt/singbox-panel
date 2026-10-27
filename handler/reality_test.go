package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/briqt/singbox-panel/model"
)

// A candidate that never answers is the most important row in the table: it is
// the reason the operator asked. Dropping it would make a dead target look like
// a target that was simply not considered.
func TestProbeAllRealityTargetsReportsSilentCandidates(t *testing.T) {
	rows := probeAllRealityTargets("www.apple.com 2 200 0.090\n")
	if len(rows) != len(realitySNIs) {
		t.Fatalf("rows=%d want one per candidate (%d)", len(rows), len(realitySNIs))
	}
	byHost := map[string]realityTargetProbe{}
	for _, row := range rows {
		byHost[row.Host] = row
	}
	if !byHost["www.apple.com"].Qualifies {
		t.Fatal("apple should qualify")
	}
	silent, ok := byHost["www.bing.com"]
	if !ok {
		t.Fatal("a candidate that never answered was dropped instead of reported")
	}
	if silent.Qualifies || silent.Note == "" {
		t.Fatalf("silent candidate should carry a note: %#v", silent)
	}
}

func TestProbeAllRealityTargetsExplainsWhyATargetFails(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantPart string
	}{
		{name: "http1", line: "www.apple.com 1.1 200 0.050\n", wantPart: "HTTP/1.1"},
		{name: "error status", line: "www.apple.com 2 403 0.050\n", wantPart: "403"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, row := range probeAllRealityTargets(tt.line) {
				if row.Host != "www.apple.com" {
					continue
				}
				if row.Qualifies {
					t.Fatalf("accepted %q", tt.line)
				}
				if !contains(row.Note, tt.wantPart) {
					t.Fatalf("note=%q does not mention %q", row.Note, tt.wantPart)
				}
				return
			}
			t.Fatalf("%q produced no row for www.apple.com", tt.line)
		})
	}
}

// A real site's root path redirects constantly. Rejecting 3xx would disqualify
// most of the candidate pool for behaving exactly like a website.
func TestProbeAllRealityTargetsAcceptsRedirects(t *testing.T) {
	for _, row := range probeAllRealityTargets("www.office.com 2 301 0.160\n") {
		if row.Host == "www.office.com" && !row.Qualifies {
			t.Fatalf("301 was rejected: %#v", row)
		}
	}
}

func TestProbeAllRealityTargetsIgnoresHostsItDidNotAskAbout(t *testing.T) {
	for _, row := range probeAllRealityTargets("evil.invalid 2 200 0.001\n") {
		if row.Host == "evil.invalid" {
			t.Fatal("a host outside the candidate list was accepted")
		}
	}
}

func TestRealitySNIOfFallsBackToHandshakeServer(t *testing.T) {
	withSNI := model.NodeInbound{Settings: json.RawMessage(`{"sni":"www.apple.com","handshake_server":"www.apple.com"}`)}
	if got := realitySNIOf(withSNI); got != "www.apple.com" {
		t.Fatalf("sni=%q", got)
	}
	legacy := model.NodeInbound{Settings: json.RawMessage(`{"handshake_server":"www.bing.com"}`)}
	if got := realitySNIOf(legacy); got != "www.bing.com" {
		t.Fatalf("legacy settings sni=%q", got)
	}
}

func TestRealityTargetRejectsBadInputBeforeAnySSH(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	ops := &NodeOpsHandler{
		Nodes:  env.nodes,
		Config: &ConfigHandler{Users: env.users, Nodes: env.nodes, Access: env.access},
	}
	path := "/api/nodes/" + strconv.Itoa(node.ID) + "/reality-target"

	rec := performJSONRequest(t, http.HandlerFunc(ops.HandleRealityTarget), http.MethodPost, path,
		map[string]any{"sni": "www.apple.com; rm -rf /"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed domain: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Valid domain, but the node has no Reality inbound to retarget.
	rec = performJSONRequest(t, http.HandlerFunc(ops.HandleRealityTarget), http.MethodPost, path,
		map[string]any{"sni": "www.apple.com"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no reality inbound: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !contains(rec.Body.String(), "vless-reality") {
		t.Fatalf("error does not name the missing protocol: %s", rec.Body.String())
	}
}

func TestRealityTargetsRejectsPost(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	ops := &NodeOpsHandler{
		Nodes:  env.nodes,
		Config: &ConfigHandler{Users: env.users, Nodes: env.nodes, Access: env.access},
	}
	path := "/api/nodes/" + strconv.Itoa(node.ID) + "/reality-targets"
	rec := performJSONRequest(t, http.HandlerFunc(ops.HandleRealityTargets), http.MethodPost, path, map[string]any{})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d", rec.Code)
	}
}

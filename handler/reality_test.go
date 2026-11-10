package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestRealitySelectionProtectsCustomLinesAndRequiresUniqueDefault(t *testing.T) {
	custom := model.NodeInbound{ID: 1, NodeID: 7, Protocol: "vless-reality", Custom: true}
	managed := model.NodeInbound{ID: 2, NodeID: 7, Protocol: "vless-reality"}
	inbounds := []model.NodeInbound{custom, managed}
	target, err := selectRealityInbound(inbounds, 0)
	if err != nil || target == nil || target.ID != managed.ID {
		t.Fatalf("implicit selection adopted custom line: target=%#v err=%v", target, err)
	}
	target, err = selectRealityInbound(inbounds, custom.ID)
	if err != nil || target == nil || target.ID != custom.ID {
		t.Fatalf("explicit ID did not select custom line: target=%#v err=%v", target, err)
	}
	target, err = selectRealityInbound([]model.NodeInbound{custom}, 0)
	if err != nil || target != nil {
		t.Fatalf("custom-only node must not have an implicit default: target=%#v err=%v", target, err)
	}
	ambiguous := append(inbounds, model.NodeInbound{ID: 3, Protocol: "vless-reality"})
	if _, err := selectRealityInbound(ambiguous, 0); err == nil {
		t.Fatal("multiple non-custom Reality lines were silently resolved to the first")
	}
	if _, err := selectRealityInbound(inbounds, 999); err == nil {
		t.Fatal("an explicit foreign/missing ID fell back to the default")
	}
	wrongProtocol := append(inbounds, model.NodeInbound{ID: 4, Protocol: "hysteria2"})
	if _, err := selectRealityInbound(wrongProtocol, 4); err == nil {
		t.Fatal("a non-Reality inbound was selected for retargeting")
	}
}

func TestRealityTargetDoesNotImplicitlyModifyCustomLine(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	createTestCustomReality(t, env, node.ID)
	// 将 Config 置空，确保误入 SSH 路径时直接失败，避免测试假阳性。
	ops := &NodeOpsHandler{Nodes: env.nodes}
	rec := performJSONRequest(t, http.HandlerFunc(ops.HandleRealityTarget), http.MethodPost,
		"/api/nodes/"+strconv.Itoa(node.ID)+"/reality-target", map[string]any{"sni": "www.bing.com"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRealityEndpointsRejectAmbiguousOrForeignSelectionBeforeSSH(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	other := env.createNode(t, "node-2")
	foreign := createTestCustomReality(t, env, other.ID)
	for i, port := range []int{443, 2053} {
		if _, err := env.nodes.CreateInbound(node.ID, model.CreateInboundReq{
			Tag: "default-" + strconv.Itoa(i), Protocol: "vless-reality", Port: port, Settings: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	ops := &NodeOpsHandler{Nodes: env.nodes}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := "/api/nodes/" + strconv.Itoa(node.ID) + "/reality-target"
		handler := http.HandlerFunc(ops.HandleRealityTarget)
		if method == http.MethodGet {
			path += "s"
			handler = ops.HandleRealityTargets
		}
		for _, suffix := range []string{"", "?inbound_id=" + strconv.Itoa(foreign.ID)} {
			rec := performJSONRequest(t, handler, method, path+suffix, map[string]any{"sni": "www.bing.com"})
			if rec.Code != http.StatusConflict {
				t.Fatalf("method=%s suffix=%s status=%d body=%s", method, suffix, rec.Code, rec.Body.String())
			}
		}
	}
}

func TestRealityInboundIDAcceptsBodyOrQueryButNeverInvalidFallback(t *testing.T) {
	id := 12
	for _, tt := range []struct {
		query string
		body  *int
		want  int
		bad   bool
	}{
		{want: 0},
		{body: &id, want: id},
		{query: "?inbound_id=12", want: id},
		{query: "?inbound_id=12", body: &id, want: id},
		{query: "?inbound_id=13", body: &id, bad: true},
		{query: "?inbound_id=garbage", bad: true},
		{query: "?inbound_id=0", bad: true},
		{query: "?inbound_id=-1", bad: true},
		{query: "?inbound_id=", bad: true},
		{query: "?inbound_id=12&inbound_id=13", bad: true},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/nodes/1/reality-targets"+tt.query, nil)
		got, err := requestedRealityInboundID(req, tt.body)
		if (err != nil) != tt.bad || (!tt.bad && got != tt.want) {
			t.Fatalf("query=%q body=%v got=%d err=%v", tt.query, tt.body, got, err)
		}
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

// The candidate list decides what is measured by default, not what is allowed.
// A host that only works from one node is exactly what ?host= exists to find.
func TestProbeAllRealityTargetsForMeasuresExtraHosts(t *testing.T) {
	candidates := append(append([]string(nil), realitySNIs...), "www.example.com")
	rows := probeAllRealityTargetsFor("www.example.com 2 200 0.070\n", candidates)
	var found bool
	for _, row := range rows {
		if row.Host == "www.example.com" {
			found = true
			if !row.Qualifies {
				t.Fatalf("extra host was measured but not qualified: %#v", row)
			}
		}
	}
	if !found {
		t.Fatal("a host passed via ?host= never reached the result table")
	}
	// Hosts outside the supplied set must still be ignored.
	for _, row := range probeAllRealityTargetsFor("evil.invalid 2 200 0.001\n", candidates) {
		if row.Host == "evil.invalid" {
			t.Fatal("unrequested host accepted")
		}
	}
}

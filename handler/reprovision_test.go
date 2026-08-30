package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/briqt/singbox-panel/model"
)

func reprovisionEnv(t *testing.T) (*ReprovisionHandler, *handlerTestEnv) {
	t.Helper()
	env := newHandlerTestEnv(t)
	config := &ConfigHandler{Users: env.users, Nodes: env.nodes, Access: env.access}
	setup := &SetupHandler{Nodes: env.nodes, Config: config}
	return &ReprovisionHandler{Nodes: env.nodes, Setup: setup}, env
}

func decodeReprovision(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, body)
	}
	return out
}

func TestReprovisionDryRunListsTargetsWithoutTouchingNodes(t *testing.T) {
	handler, env := reprovisionEnv(t)
	env.createNode(t, "node-a")
	env.createNode(t, "node-b")

	rec := performJSONRequest(t, http.HandlerFunc(handler.HandleReprovision),
		http.MethodPost, "/api/batch/reprovision", map[string]any{"dry_run": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeReprovision(t, rec.Body.Bytes())
	if out["dry_run"] != true {
		t.Fatalf("dry_run flag missing: %v", out)
	}
	nodes, _ := out["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("expected both nodes planned, got %v", out["nodes"])
	}
}

// A dry run that reached SSH would defeat its purpose. The test env has no
// reachable node, so a real auto-setup attempt shows up as a failure result;
// a dry run must return none.
func TestReprovisionDryRunReportsNoPerNodeOutcome(t *testing.T) {
	handler, env := reprovisionEnv(t)
	env.createNode(t, "node-a")

	rec := performJSONRequest(t, http.HandlerFunc(handler.HandleReprovision),
		http.MethodPost, "/api/batch/reprovision", map[string]any{"dry_run": true})
	out := decodeReprovision(t, rec.Body.Bytes())
	if _, exists := out["succeeded"]; exists {
		t.Fatalf("dry run reported execution results: %v", out)
	}
}

func TestReprovisionRestrictsToRequestedNodeIDs(t *testing.T) {
	handler, env := reprovisionEnv(t)
	first := env.createNode(t, "node-a")
	env.createNode(t, "node-b")

	rec := performJSONRequest(t, http.HandlerFunc(handler.HandleReprovision),
		http.MethodPost, "/api/batch/reprovision",
		map[string]any{"dry_run": true, "node_ids": []int{first.ID}})
	out := decodeReprovision(t, rec.Body.Bytes())
	nodes, _ := out["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("node_ids was ignored: %v", out["nodes"])
	}
	only, _ := nodes[0].(map[string]any)
	if only["node"] != "node-a" {
		t.Fatalf("wrong node selected: %v", only)
	}
}

// Reporting a fleet-wide rollout as "200 OK" when every node failed is the
// failure mode this guards: the caller checks the status code, sees success,
// and never reads the body. No node in the test env is reachable, so this run
// genuinely fails everywhere.
func TestReprovisionAnswers502WhenEveryNodeFails(t *testing.T) {
	handler, env := reprovisionEnv(t)
	env.createNode(t, "node-a")

	rec := performJSONRequest(t, http.HandlerFunc(handler.HandleReprovision),
		http.MethodPost, "/api/batch/reprovision", map[string]any{})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d want 502; body=%s", rec.Code, rec.Body.String())
	}
	out := decodeReprovision(t, rec.Body.Bytes())
	if out["succeeded"] != float64(0) {
		t.Fatalf("succeeded=%v want 0", out["succeeded"])
	}
	nodes, _ := out["nodes"].([]any)
	first, _ := nodes[0].(map[string]any)
	if first["status"] != "failed" {
		t.Fatalf("per-node status=%v want failed", first["status"])
	}
	if first["error"] == "" || first["error"] == nil {
		t.Fatal("a failed node must say why")
	}
}

func TestReprovisionRejectsUnknownMode(t *testing.T) {
	handler, env := reprovisionEnv(t)
	env.createNode(t, "node-a")

	rec := performJSONRequest(t, http.HandlerFunc(handler.HandleReprovision),
		http.MethodPost, "/api/batch/reprovision", map[string]any{"mode": "trojan-everything"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestReprovisionRejectsNonPost(t *testing.T) {
	handler, _ := reprovisionEnv(t)
	rec := performJSONRequest(t, http.HandlerFunc(handler.HandleReprovision),
		http.MethodGet, "/api/batch/reprovision", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rec.Code)
	}
}

func TestReprovisionReportsNoMatchingNodes(t *testing.T) {
	handler, env := reprovisionEnv(t)
	env.createNode(t, "node-a")

	rec := performJSONRequest(t, http.HandlerFunc(handler.HandleReprovision),
		http.MethodPost, "/api/batch/reprovision", map[string]any{"node_ids": []int{9999}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSelectReprovisionTargetsSkipsNonSingboxNodes(t *testing.T) {
	nodes := []model.Node{
		{ID: 1, Name: "singbox-node", ProxyType: "singbox"},
		{ID: 2, Name: "other-node", ProxyType: "xray"},
	}
	targets := selectReprovisionTargets(nodes, nil)
	if len(targets) != 1 || targets[0].Name != "singbox-node" {
		t.Fatalf("targets=%v; auto-setup only supports singbox nodes", targets)
	}
}

// Order decides which node restarts first. It has to come from the fleet's own
// sort order rather than however the database happened to return rows, so a
// run can be read against the node list and repeated identically.
func TestSelectReprovisionTargetsIsDeterministic(t *testing.T) {
	nodes := []model.Node{
		{ID: 7, Name: "third", ProxyType: "singbox", SortOrder: 3},
		{ID: 2, Name: "first", ProxyType: "singbox", SortOrder: 1},
		{ID: 5, Name: "second", ProxyType: "singbox", SortOrder: 2},
	}
	targets := selectReprovisionTargets(nodes, nil)
	got := []string{targets[0].Name, targets[1].Name, targets[2].Name}
	want := []string{"first", "second", "third"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order=%v want %v", got, want)
		}
	}
}

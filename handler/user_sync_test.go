package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	paneldb "github.com/briqt/singbox-panel/db"
	"github.com/briqt/singbox-panel/model"
)

type fakeNodeSynchronizer struct {
	calls [][]int
	fail  bool
}

func (s *fakeNodeSynchronizer) SyncNodes(nodeIDs []int) []NodeSyncResult {
	copied := append([]int(nil), nodeIDs...)
	s.calls = append(s.calls, copied)
	results := make([]NodeSyncResult, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		result := NodeSyncResult{NodeID: nodeID, Node: "test", Status: "pushed"}
		if s.fail {
			result.Status = "error"
			result.Error = "test sync failure"
		}
		results = append(results, result)
	}
	return results
}

type handlerTestEnv struct {
	users  *model.UserStore
	nodes  *model.NodeStore
	access *model.AccessStore
	syncer *fakeNodeSynchronizer
}

func newHandlerTestEnv(t *testing.T) *handlerTestEnv {
	t.Helper()
	database, err := paneldb.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return &handlerTestEnv{
		users:  &model.UserStore{DB: database},
		nodes:  &model.NodeStore{DB: database},
		access: &model.AccessStore{DB: database},
		syncer: &fakeNodeSynchronizer{},
	}
}

func (e *handlerTestEnv) createNode(t *testing.T, name string) *model.Node {
	t.Helper()
	node, err := e.nodes.Create(model.CreateNodeReq{Name: name, Host: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func performJSONRequest(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestCombinedUserEditUpdatesAccessAndSyncsAffectedNodes(t *testing.T) {
	env := newHandlerTestEnv(t)
	node1 := env.createNode(t, "node-1")
	node2 := env.createNode(t, "node-2")
	user, err := env.users.CreateWithPassword("new-user", "hash")
	if err != nil {
		t.Fatal(err)
	}
	userHandler := &UserHandler{Store: env.users, Access: env.access, Sync: env.syncer}

	path := "/api/users/" + strconv.Itoa(user.ID)
	rec := performJSONRequest(t, http.HandlerFunc(userHandler.ServeHTTP), http.MethodPut, path, map[string]any{
		"enabled":  true,
		"node_ids": []int{node1.ID},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("enable and assign: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(env.syncer.calls, [][]int{{node1.ID}}) {
		t.Fatalf("unexpected sync calls: %#v", env.syncer.calls)
	}
	updated, _ := env.users.Get(user.ID)
	if !updated.Enabled {
		t.Fatal("user was not enabled")
	}
	accessIDs, _ := env.access.ListNodeIDs(user.ID)
	if !reflect.DeepEqual(accessIDs, []int{node1.ID}) {
		t.Fatalf("unexpected access: %#v", accessIDs)
	}

	rec = performJSONRequest(t, http.HandlerFunc(userHandler.ServeHTTP), http.MethodPut, path, map[string]any{
		"enabled":  true,
		"node_ids": []int{node2.ID},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("replace access: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(env.syncer.calls[1], []int{node1.ID, node2.ID}) {
		t.Fatalf("old and new nodes must sync, got %#v", env.syncer.calls[1])
	}

	rec = performJSONRequest(t, http.HandlerFunc(userHandler.ServeHTTP), http.MethodPut, path, map[string]any{
		"enabled":  true,
		"node_ids": []int{node2.ID},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("retry same edit: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(env.syncer.calls[2], []int{node2.ID}) {
		t.Fatalf("save and sync must allow retrying unchanged assignments, got %#v", env.syncer.calls[2])
	}
}

func TestStandaloneAccessChangesSyncAfterUserAlreadyEnabled(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	user, err := env.users.CreateWithPassword("new-user", "hash")
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := env.users.Update(user.ID, model.UpdateUserReq{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	accessHandler := &AccessHandler{Access: env.access, Nodes: env.nodes, Sync: env.syncer}
	path := "/api/users/" + strconv.Itoa(user.ID) + "/access"

	rec := performJSONRequest(t, http.HandlerFunc(accessHandler.ServeHTTP), http.MethodPost, path, map[string]any{
		"node_id": node.ID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("grant: status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = performJSONRequest(t, http.HandlerFunc(accessHandler.ServeHTTP), http.MethodDelete, path, map[string]any{
		"node_id": node.ID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(env.syncer.calls, [][]int{{node.ID}, {node.ID}}) {
		t.Fatalf("grant and revoke must both sync: %#v", env.syncer.calls)
	}
}

func TestCombinedUserEditIsAtomicWhenNodeIsInvalid(t *testing.T) {
	env := newHandlerTestEnv(t)
	user, err := env.users.CreateWithPassword("new-user", "hash")
	if err != nil {
		t.Fatal(err)
	}
	userHandler := &UserHandler{Store: env.users, Access: env.access, Sync: env.syncer}

	path := "/api/users/" + strconv.Itoa(user.ID)
	rec := performJSONRequest(t, http.HandlerFunc(userHandler.ServeHTTP), http.MethodPut, path, map[string]any{
		"enabled":  true,
		"node_ids": []int{99999},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	unchanged, _ := env.users.Get(user.ID)
	if unchanged.Enabled {
		t.Fatal("user update was committed despite invalid node assignment")
	}
	accessIDs, _ := env.access.ListNodeIDs(user.ID)
	if len(accessIDs) != 0 {
		t.Fatalf("access update was committed despite invalid node: %#v", accessIDs)
	}
	if len(env.syncer.calls) != 0 {
		t.Fatalf("invalid edit must not sync: %#v", env.syncer.calls)
	}
}

func TestCombinedUserEditRollsBackWhenNodeSyncFails(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	user, err := env.users.CreateWithPassword("new-user", "hash")
	if err != nil {
		t.Fatal(err)
	}
	env.syncer.fail = true
	userHandler := &UserHandler{Store: env.users, Access: env.access, Sync: env.syncer}

	rec := performJSONRequest(t, http.HandlerFunc(userHandler.ServeHTTP), http.MethodPut, "/api/users/"+strconv.Itoa(user.ID), map[string]any{
		"enabled":  true,
		"node_ids": []int{node.ID},
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	unchanged, _ := env.users.Get(user.ID)
	if unchanged.Enabled {
		t.Fatal("failed synchronization left user enabled")
	}
	accessIDs, _ := env.access.ListNodeIDs(user.ID)
	if len(accessIDs) != 0 {
		t.Fatalf("failed synchronization left access behind: %#v", accessIDs)
	}
	if !reflect.DeepEqual(env.syncer.calls, [][]int{{node.ID}, {node.ID}}) {
		t.Fatalf("expected change and rollback sync attempts, got %#v", env.syncer.calls)
	}
}

func TestStandaloneAccessRollsBackWhenNodeSyncFails(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	user, err := env.users.CreateWithPassword("new-user", "hash")
	if err != nil {
		t.Fatal(err)
	}
	env.syncer.fail = true
	accessHandler := &AccessHandler{Access: env.access, Nodes: env.nodes, Sync: env.syncer}

	rec := performJSONRequest(t, http.HandlerFunc(accessHandler.ServeHTTP), http.MethodPost, "/api/users/"+strconv.Itoa(user.ID)+"/access", map[string]any{
		"node_id": node.ID,
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	accessIDs, _ := env.access.ListNodeIDs(user.ID)
	if len(accessIDs) != 0 {
		t.Fatalf("failed synchronization left access behind: %#v", accessIDs)
	}
}

func TestDeleteUserRollsBackWhenNodeSyncFails(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	user, err := env.users.CreateWithPassword("new-user", "hash")
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := env.users.UpdateWithAccess(user.ID, model.UpdateUserReq{Enabled: &enabled}, []int{node.ID}); err != nil {
		t.Fatal(err)
	}
	env.syncer.fail = true
	userHandler := &UserHandler{Store: env.users, Access: env.access, Sync: env.syncer}

	rec := performJSONRequest(t, http.HandlerFunc(userHandler.ServeHTTP), http.MethodDelete, "/api/users/"+strconv.Itoa(user.ID), nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	restored, err := env.users.Get(user.ID)
	if err != nil || !restored.Enabled {
		t.Fatalf("user was not restored: user=%#v err=%v", restored, err)
	}
	accessIDs, _ := env.access.ListNodeIDs(user.ID)
	if !reflect.DeepEqual(accessIDs, []int{node.ID}) {
		t.Fatalf("user access was not restored: %#v", accessIDs)
	}
}

func TestResetTrafficRollsBackWhenNodeSyncFails(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	user, err := env.users.CreateWithPassword("new-user", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := env.access.Grant(user.ID, node.ID); err != nil {
		t.Fatal(err)
	}
	env.users.AddTraffic(user.ID, 100, 200)
	before, _ := env.users.Get(user.ID)
	env.syncer.fail = true
	userHandler := &UserHandler{Store: env.users, Access: env.access, Sync: env.syncer}

	rec := performJSONRequest(t, http.HandlerFunc(userHandler.ServeHTTP), http.MethodPost, "/api/users/"+strconv.Itoa(user.ID)+"/reset-traffic", nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	restored, _ := env.users.Get(user.ID)
	if restored.TrafficUsedBytes != before.TrafficUsedBytes ||
		restored.TrafficUpBytes != before.TrafficUpBytes ||
		restored.TrafficDownBytes != before.TrafficDownBytes {
		t.Fatalf("traffic was not restored: before=%#v after=%#v", before, restored)
	}
}

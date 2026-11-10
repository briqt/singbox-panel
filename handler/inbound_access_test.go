package handler

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/briqt/singbox-panel/model"
)

func requireUserInboundIDs(t *testing.T, env *handlerTestEnv, userID int, want []int) {
	t.Helper()
	ids, err := env.access.ListInboundIDs(userID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("exact inbound grants=%v want=%v", ids, want)
	}
}

func TestNativeUserInboundEditOnlySyncsAddedNode(t *testing.T) {
	env := newHandlerTestEnv(t)
	la := env.createNode(t, "LA")
	tokyo := env.createNode(t, "Tokyo")
	laInbound := env.createAccessInbound(t, la.ID, "vless-reality", 443)
	tokyoInbound := env.createAccessInbound(t, tokyo.ID, "hysteria2", 8443)
	user, err := env.users.Create(model.CreateUserReq{Name: "existing", UUID: "unchanged-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.access.ReplaceInbounds(user.ID, []int{laInbound.ID}); err != nil {
		t.Fatal(err)
	}
	h := &UserHandler{Store: env.users, Access: env.access, Sync: env.syncer}
	rec := performJSONRequest(t, h, http.MethodPut, "/api/users/"+strconv.Itoa(user.ID), map[string]any{
		"inbound_ids": []int{laInbound.ID, tokyoInbound.ID},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(env.syncer.calls, [][]int{{tokyo.ID}}) {
		t.Fatalf("adding Tokyo must not restart LA: %v", env.syncer.calls)
	}
	requireUserInboundIDs(t, env, user.ID, []int{laInbound.ID, tokyoInbound.ID})
	updated, err := env.users.Get(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.UUID != user.UUID || updated.SubToken != user.SubToken {
		t.Fatalf("authorization edit changed credentials: before=%+v after=%+v", user, updated)
	}
}

func TestNativeInboundReplacementOnSameNodeStillSyncs(t *testing.T) {
	for _, endpoint := range []string{"user", "access"} {
		t.Run(endpoint, func(t *testing.T) {
			env := newHandlerTestEnv(t)
			node := env.createNode(t, "node")
			first := env.createAccessInbound(t, node.ID, "vless-reality", 443)
			second := env.createAccessInbound(t, node.ID, "hysteria2", 8443)
			user, err := env.users.Create(model.CreateUserReq{Name: "same-node"})
			if err != nil {
				t.Fatal(err)
			}
			if err := env.access.ReplaceInbounds(user.ID, []int{first.ID}); err != nil {
				t.Fatal(err)
			}
			var h http.Handler = &UserHandler{Store: env.users, Access: env.access, Sync: env.syncer}
			path := "/api/users/" + strconv.Itoa(user.ID)
			if endpoint == "access" {
				h = &AccessHandler{Access: env.access, Nodes: env.nodes, Sync: env.syncer}
				path += "/access"
			}
			rec := performJSONRequest(t, h, http.MethodPut, path, map[string]any{"inbound_ids": []int{second.ID}})
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if !reflect.DeepEqual(env.syncer.calls, [][]int{{node.ID}}) {
				t.Fatalf("changing inbound within one node must sync that node: %v", env.syncer.calls)
			}
			requireUserInboundIDs(t, env, user.ID, []int{second.ID})
		})
	}
}

func TestUserAndAccessFailureRollbacksRestoreExactInboundGrants(t *testing.T) {
	for _, operation := range []string{"edit-user", "delete-user", "replace-access", "grant-inbound", "revoke-all", "revoke-inbound"} {
		t.Run(operation, func(t *testing.T) {
			env := newHandlerTestEnv(t)
			node := env.createNode(t, "node")
			first := env.createAccessInbound(t, node.ID, "vless-reality", 443)
			second := env.createAccessInbound(t, node.ID, "hysteria2", 8443)
			user, err := env.users.Create(model.CreateUserReq{Name: "original", Note: "original note"})
			if err != nil {
				t.Fatal(err)
			}
			if err := env.access.ReplaceInbounds(user.ID, []int{first.ID}); err != nil {
				t.Fatal(err)
			}
			env.syncer.fail = true
			var h http.Handler = &UserHandler{Store: env.users, Access: env.access, Sync: env.syncer}
			path := "/api/users/" + strconv.Itoa(user.ID)
			method := http.MethodPut
			body := map[string]any{"inbound_ids": []int{second.ID}, "name": "changed", "note": "changed note"}
			switch operation {
			case "delete-user":
				method = http.MethodDelete
			case "replace-access", "grant-inbound", "revoke-all", "revoke-inbound":
				h = &AccessHandler{Access: env.access, Nodes: env.nodes, Sync: env.syncer}
				path += "/access"
				switch operation {
				case "grant-inbound":
					method, body = http.MethodPost, map[string]any{"inbound_id": second.ID}
				case "revoke-all":
					method, body = http.MethodDelete, map[string]any{"all": true}
				case "revoke-inbound":
					method, body = http.MethodDelete, map[string]any{"inbound_id": first.ID}
				}
			}
			rec := performJSONRequest(t, h, method, path, body)
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			requireUserInboundIDs(t, env, user.ID, []int{first.ID})
			restored, err := env.users.Get(user.ID)
			if err != nil {
				t.Fatal(err)
			}
			if restored.Name != user.Name || restored.Note != user.Note || restored.Enabled != user.Enabled || restored.UUID != user.UUID || restored.SubToken != user.SubToken {
				t.Fatalf("rollback did not restore user: before=%+v after=%+v", user, restored)
			}
			if !reflect.DeepEqual(env.syncer.calls, [][]int{{node.ID}, {node.ID}}) {
				t.Fatalf("expected edit and rollback synchronization on same node: %v", env.syncer.calls)
			}
			if ids, err := env.access.UsersForInbound(second.ID); err != nil || len(ids) != 0 {
				t.Fatalf("rollback widened grant to unauthorized protocol: ids=%v err=%v", ids, err)
			}
		})
	}
}

func TestAccessGetReturnsOnlyGrantedNodeInbounds(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node")
	env.createAccessInbound(t, node.ID, "vless-reality", 443)
	second := env.createAccessInbound(t, node.ID, "hysteria2", 8443)
	user, err := env.users.Create(model.CreateUserReq{Name: "access-list"})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.access.ReplaceInbounds(user.ID, []int{second.ID}); err != nil {
		t.Fatal(err)
	}
	h := &AccessHandler{Access: env.access, Nodes: env.nodes, Sync: env.syncer}
	rec := performJSONRequest(t, h, http.MethodGet, "/api/users/"+strconv.Itoa(user.ID)+"/access", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var inbounds []model.NodeInbound
	if err := json.Unmarshal(rec.Body.Bytes(), &inbounds); err != nil {
		t.Fatal(err)
	}
	if len(inbounds) != 1 || inbounds[0].ID != second.ID || inbounds[0].NodeID != node.ID || inbounds[0].Protocol != second.Protocol || inbounds[0].Port != second.Port {
		t.Fatalf("GET access must return exact NodeInbound records, got %+v", inbounds)
	}
}

func TestStandaloneInboundGrantAndRevokeSyncExistingNode(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node")
	first := env.createAccessInbound(t, node.ID, "vless-reality", 443)
	second := env.createAccessInbound(t, node.ID, "hysteria2", 8443)
	user, err := env.users.Create(model.CreateUserReq{Name: "grant-inbound"})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.access.ReplaceInbounds(user.ID, []int{first.ID}); err != nil {
		t.Fatal(err)
	}
	h := &AccessHandler{Access: env.access, Nodes: env.nodes, Sync: env.syncer}
	path := "/api/users/" + strconv.Itoa(user.ID) + "/access"
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		rec := performJSONRequest(t, h, method, path, map[string]any{"inbound_id": second.ID})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", method, rec.Code, rec.Body.String())
		}
	}
	requireUserInboundIDs(t, env, user.ID, []int{first.ID})
	if !reflect.DeepEqual(env.syncer.calls, [][]int{{node.ID}, {node.ID}}) {
		t.Fatalf("grant/revoke must sync even when node list is unchanged: %v", env.syncer.calls)
	}
}

func TestNativeUserEditWithInvalidInboundIsAtomic(t *testing.T) {
	env := newHandlerTestEnv(t)
	user, err := env.users.CreateWithPassword("inactive", "hash")
	if err != nil {
		t.Fatal(err)
	}
	h := &UserHandler{Store: env.users, Access: env.access, Sync: env.syncer}
	rec := performJSONRequest(t, h, http.MethodPut, "/api/users/"+strconv.Itoa(user.ID), map[string]any{
		"enabled": true, "inbound_ids": []int{99999},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	unchanged, err := env.users.Get(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Enabled || len(env.syncer.calls) != 0 {
		t.Fatalf("invalid grant partially committed: user=%+v sync=%v", unchanged, env.syncer.calls)
	}
}

func TestDeleteDatabaseFailureRestoresExactInboundAccessAndNodes(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node")
	first := env.createAccessInbound(t, node.ID, "vless-reality", 443)
	env.createAccessInbound(t, node.ID, "hysteria2", 8443)
	user, err := env.users.Create(model.CreateUserReq{Name: "not-deleted"})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.access.ReplaceInbounds(user.ID, []int{first.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.users.DB.Exec(`CREATE TRIGGER reject_user_deletion BEFORE DELETE ON users BEGIN SELECT RAISE(ABORT, 'delete failure'); END`); err != nil {
		t.Fatal(err)
	}
	h := &UserHandler{Store: env.users, Access: env.access, Sync: env.syncer}
	rec := performJSONRequest(t, h, http.MethodDelete, "/api/users/"+strconv.Itoa(user.ID), nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	requireUserInboundIDs(t, env, user.ID, []int{first.ID})
	restored, err := env.users.Get(user.ID)
	if err != nil || !restored.Enabled {
		t.Fatalf("failed deletion did not restore user: user=%+v err=%v", restored, err)
	}
	if !reflect.DeepEqual(env.syncer.calls, [][]int{{node.ID}, {node.ID}}) {
		t.Fatalf("nodes not restored after database deletion failed: %v", env.syncer.calls)
	}
}

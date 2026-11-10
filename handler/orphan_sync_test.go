package handler

import (
	"net/http"
	"strconv"
	"testing"
)

// Reproduces the production failure of 2026-11-10: nodes deleted by an older
// panel left their inbounds behind, the v1 access migration copied grants onto
// them, and every later edit of that user tried to sync the vanished node,
// failed with "node not found", and rolled back.
func TestUserEditSucceedsWhenAGrantPointsAtADeletedNode(t *testing.T) {
	env := newHandlerTestEnv(t)
	gone := env.createAccessNode(t, "deleted-long-ago")
	inbounds, _ := env.nodes.ListInbounds(gone.ID)
	user, err := env.users.CreateWithPassword("owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := env.access.GrantInbound(user.ID, inbounds[0].ID); err != nil {
		t.Fatal(err)
	}
	// What an old panel did: remove only the node row.
	if _, err := env.nodes.DB.Exec(`DELETE FROM nodes WHERE id = ?`, gone.ID); err != nil {
		t.Fatal(err)
	}

	// The real synchronizer, not the fake: the bug lived in its result.
	userHandler := &UserHandler{Store: env.users, Access: env.access, Sync: &ConfigHandler{Nodes: env.nodes, Users: env.users, Access: env.access}}
	rec := performJSONRequest(t, http.HandlerFunc(userHandler.ServeHTTP), http.MethodPut,
		"/api/users/"+strconv.Itoa(user.ID), map[string]any{"enabled": true, "inbound_ids": []int{}})
	if rec.Code != http.StatusOK {
		t.Fatalf("edit must succeed when the only affected node no longer exists: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if ids, _ := env.access.ListInboundIDs(user.ID); len(ids) != 0 {
		t.Fatalf("grant on the deleted node's inbound was not removed: %v", ids)
	}
}

func TestSyncReportsDeletedNodeAsGoneNotFailure(t *testing.T) {
	env := newHandlerTestEnv(t)
	h := &ConfigHandler{Nodes: env.nodes, Users: env.users, Access: env.access}
	results := h.SyncNodes([]int{987654})
	if len(results) != 1 || results[0].Status != "gone" {
		t.Fatalf("results = %+v", results)
	}
	if err := syncFailure(results); err != nil {
		t.Fatalf("a deleted node has nothing to sync and must not fail the caller: %v", err)
	}
	// A real failure must still be one.
	if err := syncFailure([]NodeSyncResult{{NodeID: 1, Status: "error", Error: "push: boom"}}); err == nil {
		t.Fatal("an actual push error was swallowed")
	}
}

package handler

import (
	"net/http"
	"strconv"
	"testing"
)

func TestAutoSetupRejectsInvalidRequestsBeforeSSH(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	setup := &SetupHandler{
		Nodes: env.nodes,
		Config: &ConfigHandler{
			Users: env.users, Nodes: env.nodes, Access: env.access,
		},
	}
	path := "/api/nodes/" + strconv.Itoa(node.ID) + "/auto-setup"

	tests := []struct {
		name string
		body map[string]any
	}{
		{name: "duplicate protocol", body: map[string]any{"protocols": []string{"vless-reality", "vless-reality"}}},
		{name: "domain protocol without domain", body: map[string]any{"protocols": []string{"hysteria2"}}},
		{name: "unsupported protocol", body: map[string]any{"protocols": []string{"trojan"}}},
		{name: "invalid port", body: map[string]any{"protocols": []string{"vless-reality"}, "ports": map[string]any{"reality": 70000}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := performJSONRequest(t, http.HandlerFunc(setup.HandleAutoSetup), http.MethodPost, path, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

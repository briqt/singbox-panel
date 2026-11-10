package handler

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/briqt/singbox-panel/model"
)

func TestDeleteInboundSynchronizesNode(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	inbound, err := env.nodes.CreateInbound(node.ID, model.CreateInboundReq{
		Tag: "reality", Protocol: "vless-reality", Port: 24443,
		Settings: json.RawMessage(`{"sni":"example.com","private_key":"private","public_key":"public"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	nodeHandler := &NodeHandler{Store: env.nodes, Access: env.access, Sync: env.syncer}

	rec := performJSONRequest(t, http.HandlerFunc(nodeHandler.ServeHTTP), http.MethodDelete, "/api/inbounds/"+strconv.Itoa(inbound.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(env.syncer.calls, [][]int{{node.ID}}) {
		t.Fatalf("unexpected sync calls: %#v", env.syncer.calls)
	}
	inbounds, _ := env.nodes.ListInbounds(node.ID)
	if len(inbounds) != 0 {
		t.Fatalf("inbound still exists: %#v", inbounds)
	}
}

func TestDeleteInboundRollsBackWhenSyncFails(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	inbound, err := env.nodes.CreateInbound(node.ID, model.CreateInboundReq{
		Name: "custom egress", Tag: "reality", Protocol: "vless-reality", Port: 24443, Custom: true,
		Outbound: &model.OutboundConfig{Type: "socks", Server: "127.0.0.2", ServerPort: 1080, Network: "tcp"},
		Settings: json.RawMessage(`{"sni":"example.com","private_key":"private","public_key":"public"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	userID := grantTestInbound(t, env, inbound.ID)
	env.syncer.fail = true
	nodeHandler := &NodeHandler{Store: env.nodes, Access: env.access, Sync: env.syncer}

	rec := performJSONRequest(t, http.HandlerFunc(nodeHandler.ServeHTTP), http.MethodDelete, "/api/inbounds/"+strconv.Itoa(inbound.ID), nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	inbounds, _ := env.nodes.ListInbounds(node.ID)
	if len(inbounds) != 1 || inbounds[0].Protocol != inbound.Protocol || inbounds[0].Port != inbound.Port {
		t.Fatalf("inbound was not restored: %#v", inbounds)
	}
	if !reflect.DeepEqual(inbounds[0], *inbound) {
		t.Fatalf("rollback changed the inbound: before=%#v after=%#v", inbound, inbounds[0])
	}
	assertTestInboundGrant(t, env, inbound.ID, userID)
	if !reflect.DeepEqual(env.syncer.calls, [][]int{{node.ID}, {node.ID}}) {
		t.Fatalf("rollback must also restore the remote: %#v", env.syncer.calls)
	}
}

func TestCreateInboundRollsBackWhenSyncFails(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	env.syncer.fail = true
	nodeHandler := &NodeHandler{Store: env.nodes, Access: env.access, Sync: env.syncer}

	rec := performJSONRequest(t, http.HandlerFunc(nodeHandler.ServeHTTP), http.MethodPost, "/api/nodes/"+strconv.Itoa(node.ID)+"/inbounds", map[string]any{
		"tag": "reality", "protocol": "vless-reality", "port": 24443,
		"settings": map[string]any{"sni": "example.com", "private_key": "private", "public_key": "public"},
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	inbounds, _ := env.nodes.ListInbounds(node.ID)
	if len(inbounds) != 0 {
		t.Fatalf("failed create left an inbound behind: %#v", inbounds)
	}
}

func TestNodeDomainUpdateRequiresAutoSetupForDomainBoundInbound(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	_, err := env.nodes.CreateInbound(node.ID, model.CreateInboundReq{
		Tag: "hysteria2", Protocol: "hysteria2", Port: 24443,
		Settings: json.RawMessage(`{"domain":"old.example.com","cert_path":"/old.crt","key_path":"/old.key"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	nodeHandler := &NodeHandler{Store: env.nodes, Access: env.access, Sync: env.syncer}

	rec := performJSONRequest(t, http.HandlerFunc(nodeHandler.ServeHTTP), http.MethodPut, "/api/nodes/"+strconv.Itoa(node.ID), map[string]any{
		"domain": "new.example.com",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	unchanged, _ := env.nodes.Get(node.ID)
	if unchanged.Domain != node.Domain {
		t.Fatalf("domain changed without migration: %q", unchanged.Domain)
	}
}

func TestCreateInboundRejectsUnsupportedProtocolBeforeSync(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	nodeHandler := &NodeHandler{Store: env.nodes, Access: env.access, Sync: env.syncer}

	rec := performJSONRequest(t, http.HandlerFunc(nodeHandler.ServeHTTP), http.MethodPost, "/api/nodes/"+strconv.Itoa(node.ID)+"/inbounds", map[string]any{
		"tag": "unknown", "protocol": "trojan", "port": 24443, "settings": map[string]any{},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	inbounds, _ := env.nodes.ListInbounds(node.ID)
	if len(inbounds) != 0 || len(env.syncer.calls) != 0 {
		t.Fatalf("invalid inbound changed state: inbounds=%#v sync=%#v", inbounds, env.syncer.calls)
	}
}

func TestNodeDeleteRequiresDecommissionedNode(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	inbound, err := env.nodes.CreateInbound(node.ID, model.CreateInboundReq{
		Tag: "reality", Protocol: "vless-reality", Port: 24443, Settings: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	nodeHandler := &NodeHandler{Store: env.nodes, Access: env.access, Sync: env.syncer}
	path := "/api/nodes/" + strconv.Itoa(node.ID)

	rec := performJSONRequest(t, http.HandlerFunc(nodeHandler.ServeHTTP), http.MethodDelete, path, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("node with inbound: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := env.nodes.DeleteInbound(inbound.ID); err != nil {
		t.Fatal(err)
	}
	rec = performJSONRequest(t, http.HandlerFunc(nodeHandler.ServeHTTP), http.MethodDelete, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("decommissioned node: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := env.nodes.Get(node.ID); err == nil {
		t.Fatal("node still exists")
	}
}

func TestManualRealityGetsIndependentIdentityAndNoAccess(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	original, err := env.nodes.CreateInbound(node.ID, model.CreateInboundReq{
		Name: "default", Tag: "vless-reality", Protocol: "vless-reality", Port: 443,
		Settings: json.RawMessage(`{"sni":"www.bing.com","private_key":"original-private","public_key":"original-public","short_id":"0123456789abcdef"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	grantTestInbound(t, env, original.ID)
	h := &NodeHandler{Store: env.nodes, Access: env.access, Sync: env.syncer}
	var previousPrivate, previousShort string
	for i, port := range []int{8443, 2053} {
		rec := performJSONRequest(t, h, http.MethodPost, "/api/nodes/"+strconv.Itoa(node.ID)+"/inbounds", map[string]any{
			"name": "upstream", "tag": "upstream-" + strconv.Itoa(i), "protocol": "vless-reality", "port": port,
			"custom":   false,
			"outbound": map[string]any{"type": "socks", "server": "127.0.0.2", "server_port": 1080},
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var created model.NodeInbound
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		if !created.Custom || created.Name != "upstream" || created.Outbound == nil || created.Outbound.Network != "tcp" {
			t.Fatalf("manual metadata lost: %#v", created)
		}
		var settings map[string]any
		if err := json.Unmarshal(created.Settings, &settings); err != nil {
			t.Fatal(err)
		}
		if settings["sni"] != "www.bing.com" || settings["handshake_server"] != "www.bing.com" {
			t.Fatalf("did not reuse only the default's camouflage: %#v", settings)
		}
		private, _ := settings["private_key"].(string)
		public, _ := settings["public_key"].(string)
		short, _ := settings["short_id"].(string)
		privateBytes, err := base64.RawURLEncoding.DecodeString(private)
		if err != nil {
			t.Fatal(err)
		}
		key, err := ecdh.X25519().NewPrivateKey(privateBytes)
		if err != nil || public != base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()) {
			t.Fatalf("new Reality keypair is invalid: private=%q public=%q err=%v", private, public, err)
		}
		shortBytes, err := hex.DecodeString(short)
		if err != nil || len(shortBytes) != 8 || short == "0123456789abcdef" || short == previousShort || private == previousPrivate {
			t.Fatalf("new lines must get fresh identities: settings=%#v err=%v", settings, err)
		}
		previousPrivate, previousShort = private, short
		var grants int
		if err := env.nodes.DB.QueryRow(`SELECT COUNT(*) FROM user_inbound_access WHERE inbound_id = ?`, created.ID).Scan(&grants); err != nil || grants != 0 {
			t.Fatalf("new manual line was authorized: grants=%d err=%v", grants, err)
		}
		unchanged, err := env.nodes.GetInbound(original.ID)
		if err != nil || !reflect.DeepEqual(unchanged, original) {
			t.Fatalf("creating a custom line changed the default: got=%#v err=%v", unchanged, err)
		}
	}
}

func TestManualInboundConflictsArePerNetwork(t *testing.T) {
	tests := []struct {
		name     string
		tag      string
		protocol string
		port     int
		outbound map[string]any
		want     int
	}{
		{name: "same TCP port", tag: "other", protocol: "vless-reality", port: 443, want: http.StatusConflict},
		{name: "different TCP protocol", tag: "other", protocol: "vless-httpupgrade", port: 443, want: http.StatusConflict},
		{name: "TCP and UDP coexist", tag: "udp", protocol: "hysteria2", port: 443, want: http.StatusCreated},
		{name: "duplicate tag", tag: "default", protocol: "vless-reality", port: 8443, want: http.StatusConflict},
		{name: "empty tag", protocol: "vless-reality", port: 8443, want: http.StatusBadRequest},
		{name: "unsupported outbound", tag: "other", protocol: "vless-reality", port: 8443, outbound: map[string]any{"type": "http", "server": "127.0.0.2", "server_port": 1080}, want: http.StatusBadRequest},
		{name: "UDP outbound", tag: "other", protocol: "vless-reality", port: 8443, outbound: map[string]any{"type": "socks", "server": "127.0.0.2", "server_port": 1080, "network": "udp"}, want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newHandlerTestEnv(t)
			node := env.createNode(t, "node-1")
			if _, err := env.nodes.CreateInbound(node.ID, model.CreateInboundReq{
				Tag: "default", Protocol: "vless-reality", Port: 443, Settings: json.RawMessage(`{}`),
			}); err != nil {
				t.Fatal(err)
			}
			h := &NodeHandler{Store: env.nodes, Access: env.access, Sync: env.syncer}
			rec := performJSONRequest(t, h, http.MethodPost, "/api/nodes/"+strconv.Itoa(node.ID)+"/inbounds", map[string]any{
				"tag": tt.tag, "protocol": tt.protocol, "port": tt.port, "outbound": tt.outbound,
				"settings": map[string]any{"sni": "example.com", "private_key": "private", "public_key": "public", "domain": "example.com", "cert_path": "/cert", "key_path": "/key"},
			})
			if rec.Code != tt.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tt.want, rec.Body.String())
			}
			if tt.want != http.StatusCreated && len(env.syncer.calls) != 0 {
				t.Fatalf("invalid request was pushed: %#v", env.syncer.calls)
			}
		})
	}
}

func TestUpdateInboundPreservesSettingsIdentityAndOwnership(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	inbound := createTestCustomReality(t, env, node.ID)
	userID := grantTestInbound(t, env, inbound.ID)
	h := &NodeHandler{Store: env.nodes, Access: env.access, Sync: env.syncer}
	path := "/api/inbounds/" + strconv.Itoa(inbound.ID)
	rec := performJSONRequest(t, h, http.MethodPut, path, map[string]any{
		"name": "renamed", "port": 2053, "custom": false,
		"settings": map[string]any{"sni": "www.bing.com", "handshake_server": "www.bing.com"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	updated, err := env.nodes.GetInbound(inbound.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "renamed" || updated.Port != 2053 || !updated.Custom || !reflect.DeepEqual(updated.Outbound, inbound.Outbound) || updated.Tag != inbound.Tag {
		t.Fatalf("patch lost metadata: %#v", updated)
	}
	var settings map[string]any
	if err := json.Unmarshal(updated.Settings, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["private_key"] != "private" || settings["public_key"] != "public" || settings["short_id"] != "0123456789abcdef" || settings["fingerprint"] != "firefox" || settings["sni"] != "www.bing.com" {
		t.Fatalf("patch replaced unspecified settings or identity: %#v", settings)
	}
	assertTestInboundGrant(t, env, inbound.ID, userID)

	// 替换后的代理不得继承旧代理的凭据。
	rec = performJSONRequest(t, h, http.MethodPut, path, map[string]any{
		"outbound": map[string]any{"type": "socks", "server": "127.0.0.3", "server_port": 1081},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("replace egress: status=%d body=%s", rec.Code, rec.Body.String())
	}
	replaced, err := env.nodes.GetInbound(inbound.ID)
	if err != nil || replaced.Outbound == nil || replaced.Outbound.Server != "127.0.0.3" || replaced.Outbound.Username != "" || replaced.Outbound.Password != "" || replaced.Outbound.Network != "tcp" {
		t.Fatalf("replacement egress inherited credentials: %#v err=%v", replaced, err)
	}
	// 未传出口时保持不变；显式传入 null 时将该线路切回直连。
	rec = performJSONRequest(t, h, http.MethodPut, path, map[string]any{"outbound": nil})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear egress: status=%d body=%s", rec.Code, rec.Body.String())
	}
	cleared, err := env.nodes.GetInbound(inbound.ID)
	if err != nil || cleared.Outbound != nil || !reflect.DeepEqual(cleared.Settings, updated.Settings) {
		t.Fatalf("clearing egress changed the identity: %#v err=%v", cleared, err)
	}
}

func TestUpdateInboundRejectsIdentityAndPortConflicts(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
		want int
	}{
		{name: "private key", body: map[string]any{"settings": map[string]any{"private_key": "rotated"}}, want: http.StatusBadRequest},
		{name: "short ID", body: map[string]any{"settings": map[string]any{"short_id": "ffffffffffffffff"}}, want: http.StatusBadRequest},
		{name: "protocol", body: map[string]any{"protocol": "hysteria2"}, want: http.StatusBadRequest},
		{name: "invalid outbound", body: map[string]any{"outbound": map[string]any{"type": "http"}}, want: http.StatusBadRequest},
		{name: "TCP port", body: map[string]any{"port": 443}, want: http.StatusConflict},
		{name: "tag", body: map[string]any{"tag": "default"}, want: http.StatusConflict},
		{name: "null settings", body: map[string]any{"settings": nil}, want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newHandlerTestEnv(t)
			node := env.createNode(t, "node-1")
			inbound := createTestCustomReality(t, env, node.ID)
			if _, err := env.nodes.CreateInbound(node.ID, model.CreateInboundReq{Tag: "default", Protocol: "vless-reality", Port: 443, Settings: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
			h := &NodeHandler{Store: env.nodes, Access: env.access, Sync: env.syncer}
			rec := performJSONRequest(t, h, http.MethodPut, "/api/inbounds/"+strconv.Itoa(inbound.ID), tt.body)
			if rec.Code != tt.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tt.want, rec.Body.String())
			}
			unchanged, err := env.nodes.GetInbound(inbound.ID)
			if err != nil || !reflect.DeepEqual(unchanged, inbound) || len(env.syncer.calls) != 0 {
				t.Fatalf("rejected patch changed state: %#v err=%v sync=%#v", unchanged, err, env.syncer.calls)
			}
		})
	}
}

func TestUpdateInboundRollbackRestoresDatabaseRemoteAndAccess(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	other := env.createNode(t, "untouched")
	inbound := createTestCustomReality(t, env, node.ID)
	userID := grantTestInbound(t, env, inbound.ID)
	calls := 0
	syncer := inboundTestSynchronizer(func(ids []int) []NodeSyncResult {
		calls++
		if !reflect.DeepEqual(ids, []int{node.ID}) {
			t.Fatalf("update touched another node %d: %#v", other.ID, ids)
		}
		current, err := env.nodes.GetInbound(inbound.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertTestInboundGrant(t, env, inbound.ID, userID)
		if calls == 1 {
			if current.Name != "changed" || current.Port != 2053 || current.Outbound == nil || current.Outbound.Server != "127.0.0.3" {
				t.Fatalf("push did not see the edited line: %#v", current)
			}
			return []NodeSyncResult{{NodeID: node.ID, Status: "error", Error: "push failed"}}
		}
		if !reflect.DeepEqual(current, inbound) {
			t.Fatalf("restoring the remote saw the wrong snapshot: %#v", current)
		}
		return []NodeSyncResult{{NodeID: node.ID, Status: "pushed"}}
	})
	h := &NodeHandler{Store: env.nodes, Access: env.access, Sync: syncer}
	rec := performJSONRequest(t, h, http.MethodPut, "/api/inbounds/"+strconv.Itoa(inbound.ID), map[string]any{
		"name": "changed", "port": 2053,
		"outbound": map[string]any{"type": "socks", "server": "127.0.0.3", "server_port": 1081},
	})
	if rec.Code != http.StatusBadGateway || calls != 2 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, calls, rec.Body.String())
	}
	assertTestInboundGrant(t, env, inbound.ID, userID)
}

type inboundTestSynchronizer func([]int) []NodeSyncResult

func (s inboundTestSynchronizer) SyncNodes(ids []int) []NodeSyncResult { return s(ids) }

func createTestCustomReality(t *testing.T, env *handlerTestEnv, nodeID int) *model.NodeInbound {
	t.Helper()
	inbound, err := env.nodes.CreateInbound(nodeID, model.CreateInboundReq{
		Name: "custom", Tag: "upstream", Protocol: "vless-reality", Port: 8443, Custom: true,
		Outbound: &model.OutboundConfig{Type: "socks", Server: "127.0.0.2", ServerPort: 1080, Username: "user", Password: "password", Network: "tcp"},
		Settings: json.RawMessage(`{"sni":"www.apple.com","handshake_server":"www.apple.com","private_key":"private","public_key":"public","short_id":"0123456789abcdef","fingerprint":"firefox"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return inbound
}

func grantTestInbound(t *testing.T, env *handlerTestEnv, inboundID int) int {
	t.Helper()
	user, err := env.users.CreateWithPassword("user-"+strconv.Itoa(inboundID), "hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.nodes.DB.Exec(`INSERT INTO user_inbound_access (user_id, inbound_id) VALUES (?, ?)`, user.ID, inboundID); err != nil {
		t.Fatal(err)
	}
	return user.ID
}

func assertTestInboundGrant(t *testing.T, env *handlerTestEnv, inboundID, userID int) {
	t.Helper()
	var grants int
	if err := env.nodes.DB.QueryRow(`SELECT COUNT(*) FROM user_inbound_access WHERE user_id = ? AND inbound_id = ?`, userID, inboundID).Scan(&grants); err != nil || grants != 1 {
		t.Fatalf("inbound access was lost: grants=%d err=%v", grants, err)
	}
}

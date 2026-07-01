package handler

import (
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
		Tag: "reality", Protocol: "vless-reality", Port: 24443,
		Settings: json.RawMessage(`{"sni":"example.com","private_key":"private","public_key":"public"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
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
	if inbounds[0].ID != inbound.ID {
		t.Fatalf("rollback changed inbound id: before=%d after=%d", inbound.ID, inbounds[0].ID)
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

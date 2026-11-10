package handler

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/briqt/singbox-panel/model"
)

func createViaAPI(t *testing.T, env *handlerTestEnv, nodeID int, body map[string]any) (int, map[string]any) {
	t.Helper()
	h := &NodeHandler{Store: env.nodes, Access: env.access, Sync: env.syncer}
	rec := performJSONRequest(t, h, http.MethodPost, "/api/nodes/"+strconv.Itoa(nodeID)+"/inbounds", body)
	var decoded map[string]any
	json.Unmarshal(rec.Body.Bytes(), &decoded)
	return rec.Code, decoded
}

func TestCreateRelayGeneratesKeyAndRequiresSources(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "laxcg")

	code, body := createViaAPI(t, env, node.ID, map[string]any{"tag": "relay", "protocol": "shadowsocks", "port": 8443})
	if code != http.StatusBadRequest {
		t.Fatalf("缺少来源白名单必须拒绝：%d %v", code, body)
	}
	code, body = createViaAPI(t, env, node.ID, map[string]any{"tag": "relay", "protocol": "shadowsocks", "port": 8443,
		"settings": map[string]any{"allowed_sources": []string{"203.0.113.4"}},
		"outbound": map[string]any{"type": "socks", "server": "192.0.2.1", "server_port": 1080}})
	if code != http.StatusBadRequest {
		t.Fatalf("落地入站不能再串上游：%d %v", code, body)
	}
	if len(env.syncer.calls) != 0 {
		t.Fatalf("无效请求不应推送：%v", env.syncer.calls)
	}

	code, body = createViaAPI(t, env, node.ID, map[string]any{"tag": "relay", "protocol": "shadowsocks", "port": 8443,
		"settings": map[string]any{"allowed_sources": []string{"203.0.113.4", "198.51.100.0/24"}}})
	if code != http.StatusCreated {
		t.Fatalf("status=%d body=%v", code, body)
	}
	inbound, err := env.nodes.GetInbound(int(body["id"].(float64)))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	json.Unmarshal(inbound.Settings, &settings)
	key, _ := base64.StdEncoding.DecodeString(settings["password"].(string))
	if settings["method"] != model.DefaultSS2022Method || len(key) != 16 || !inbound.Custom {
		t.Fatalf("应生成 16 字节 SS2022 密钥并标记为自定义：%v custom=%v", settings, inbound.Custom)
	}

	// 方法与密钥是身份：改了会让所有入口机的出口失效。
	h := &NodeHandler{Store: env.nodes, Access: env.access, Sync: env.syncer}
	for _, patch := range []map[string]any{
		{"password": base64.StdEncoding.EncodeToString(make([]byte, 16))},
		{"method": "2022-blake3-aes-256-gcm"},
	} {
		rec := performJSONRequest(t, h, http.MethodPut, "/api/inbounds/"+strconv.Itoa(inbound.ID), map[string]any{"settings": patch})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("身份字段 %v 不应可改：%d %s", patch, rec.Code, rec.Body.String())
		}
	}
	// 白名单可以调整。
	rec := performJSONRequest(t, h, http.MethodPut, "/api/inbounds/"+strconv.Itoa(inbound.ID), map[string]any{"settings": map[string]any{"allowed_sources": []string{"203.0.113.9"}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("白名单应可调整：%d %s", rec.Code, rec.Body.String())
	}
	after, _ := env.nodes.GetInbound(inbound.ID)
	var afterSettings map[string]any
	json.Unmarshal(after.Settings, &afterSettings)
	if afterSettings["password"] != settings["password"] || !reflect.DeepEqual(afterSettings["allowed_sources"], []any{"203.0.113.9"}) {
		t.Fatalf("调整白名单不应动密钥：%v", afterSettings)
	}
}

// SS 落地同时占用 TCP 与 UDP：同端口的 hysteria2 或 Reality 都必须被挡在推送之前。
func TestRelayPortConflictsOnBothNetworks(t *testing.T) {
	for _, existing := range []string{"hysteria2", "vless-reality"} {
		t.Run(existing, func(t *testing.T) {
			env := newHandlerTestEnv(t)
			node := env.createNode(t, "laxcg")
			if _, err := env.nodes.CreateInbound(node.ID, model.CreateInboundReq{Tag: existing, Protocol: existing, Port: 443, Settings: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
			code, body := createViaAPI(t, env, node.ID, map[string]any{"tag": "relay", "protocol": "shadowsocks", "port": 443,
				"settings": map[string]any{"allowed_sources": []string{"203.0.113.4"}}})
			if code != http.StatusConflict {
				t.Fatalf("与 %s 同端口必须冲突：%d %v", existing, code, body)
			}
		})
	}
}

func TestCustomHysteria2CopiesDefaultProfile(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "tokyo")
	code, _ := createViaAPI(t, env, node.ID, map[string]any{"tag": "hysteria2-laxcg", "protocol": "hysteria2", "port": 2053})
	if code != http.StatusBadRequest {
		t.Fatalf("没有默认 hysteria2 时不应凭空生成证书路径：%d", code)
	}
	defaultSettings := `{"profile":"salamander","domain":"tokyo.example.com","cert_path":"/c","key_path":"/k","obfs_password":"obfs-secret"}`
	if _, err := env.nodes.CreateInbound(node.ID, model.CreateInboundReq{Tag: "hysteria2", Protocol: "hysteria2", Port: 8443, Settings: json.RawMessage(defaultSettings)}); err != nil {
		t.Fatal(err)
	}
	ssKey := base64.StdEncoding.EncodeToString(make([]byte, 16))
	code, body := createViaAPI(t, env, node.ID, map[string]any{"tag": "hysteria2-laxcg", "protocol": "hysteria2", "port": 2053,
		"outbound": map[string]any{"type": "shadowsocks", "server": "198.51.100.7", "server_port": 8443, "method": model.DefaultSS2022Method, "password": ssKey}})
	if code != http.StatusCreated {
		t.Fatalf("status=%d body=%v", code, body)
	}
	inbound, _ := env.nodes.GetInbound(int(body["id"].(float64)))
	var got, want map[string]any
	json.Unmarshal(inbound.Settings, &got)
	json.Unmarshal([]byte(defaultSettings), &want)
	if !reflect.DeepEqual(got, want) || !inbound.Custom {
		t.Fatalf("应完整沿用默认 hysteria2 的域名/证书/混淆：%v", got)
	}
	// SS 出口的网络保持为空（TCP+UDP），不能像 SOCKS5 那样被改写成 tcp。
	if inbound.Outbound == nil || inbound.Outbound.Network != "" || inbound.Outbound.Type != "shadowsocks" {
		t.Fatalf("SS 出口被改写：%+v", inbound.Outbound)
	}
	code, _ = createViaAPI(t, env, node.ID, map[string]any{"tag": "dup-port", "protocol": "hysteria2", "port": 8443})
	if code != http.StatusConflict {
		t.Fatalf("复制线路不能占用默认线路的 UDP 端口：%d", code)
	}
}

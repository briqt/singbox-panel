package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/briqt/singbox-panel/model"
)

func TestRawConfigWriteIsDisabled(t *testing.T) {
	handler := &ConfigHandler{}
	req := httptest.NewRequest(http.MethodPut, "/api/nodes/1/raw-config", strings.NewReader(`{"inbounds":[]}`))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("Allow=%q", rec.Header().Get("Allow"))
	}
	if !strings.Contains(rec.Body.String(), "read-only") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestAtomicDeployCommandRestoresPreviousConfigOnRestartFailure(t *testing.T) {
	command := buildAtomicDeployCommand("/etc/sing-box/config.json", "/tmp/panel-config.json")
	for _, fragment := range []string{
		"cp /etc/sing-box/config.json /tmp/panel-config.json.backup",
		"cp /tmp/panel-config.json /etc/sing-box/config.json",
		"cp /tmp/panel-config.json.backup /etc/sing-box/config.json",
		"systemctl restart sing-box",
	} {
		if !strings.Contains(command, fragment) {
			t.Fatalf("deploy command is missing %q:\n%s", fragment, command)
		}
	}
}

func TestGenerateConfigInjectsOnlyEachInboundsAuthorizedActiveUsers(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "shared")
	first := env.createAccessInbound(t, node.ID, "vless-reality", 443)
	second := env.createAccessInbound(t, node.ID, "hysteria2", 8443)
	userA, err := env.users.Create(model.CreateUserReq{Name: "only-A"})
	if err != nil {
		t.Fatal(err)
	}
	userB, err := env.users.Create(model.CreateUserReq{Name: "only-B"})
	if err != nil {
		t.Fatal(err)
	}
	for userID, inboundID := range map[int]int{userA.ID: first.ID, userB.ID: second.ID} {
		if err := env.access.ReplaceInbounds(userID, []int{inboundID}); err != nil {
			t.Fatal(err)
		}
		if _, err := env.users.DB.Exec(`INSERT INTO user_access (user_id, node_id) VALUES (?, ?)`, userID, node.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, state := range []string{"disabled", "expired", "exhausted"} {
		user, err := env.users.Create(model.CreateUserReq{Name: state})
		if err != nil {
			t.Fatal(err)
		}
		if err := env.access.ReplaceInbounds(user.ID, []int{first.ID, second.ID}); err != nil {
			t.Fatal(err)
		}
		switch state {
		case "disabled":
			enabled := false
			_, err = env.users.Update(user.ID, model.UpdateUserReq{Enabled: &enabled})
		case "expired":
			expireAt := "2000-01-01 00:00:00"
			_, err = env.users.Update(user.ID, model.UpdateUserReq{ExpireAt: &expireAt})
		case "exhausted":
			limit := int64(1)
			_, err = env.users.Update(user.ID, model.UpdateUserReq{TrafficLimitBytes: &limit})
			env.users.AddTraffic(user.ID, 1, 0)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	// 未授权入站与 A 同节点、同协议，但既不能继承 A 的用户，
	// 也不能开启无需认证的监听。
	env.createAccessInbound(t, node.ID, "vless-reality", 9443)
	h := &ConfigHandler{Users: env.users, Nodes: env.nodes, Access: env.access}
	data, err := h.generateConfig(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Inbounds []struct {
			Port  int `json:"listen_port"`
			Users []struct {
				Name     string `json:"name"`
				UUID     string `json:"uuid"`
				Password string `json:"password"`
			} `json:"users"`
		} `json:"inbounds"`
		Experimental struct {
			V2RayAPI struct {
				Stats struct {
					Users []string `json:"users"`
				} `json:"stats"`
			} `json:"v2ray_api"`
		} `json:"experimental"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Inbounds) != 2 {
		t.Fatalf("unexpected listeners, including unassigned inbound: %s", data)
	}
	want := map[int]*model.User{first.Port: userA, second.Port: userB}
	for _, inbound := range config.Inbounds {
		user, ok := want[inbound.Port]
		if !ok || len(inbound.Users) != 1 {
			t.Fatalf("unexpected inbound/users: %+v", inbound)
		}
		credential := inbound.Users[0].UUID
		if inbound.Port == second.Port {
			credential = inbound.Users[0].Password
		}
		if inbound.Users[0].Name != user.Name || credential != user.UUID {
			t.Fatalf("user or exact credential leaked across protocols: %+v", inbound)
		}
	}
	if !reflect.DeepEqual(config.Experimental.V2RayAPI.Stats.Users, []string{userA.Name, userB.Name}) {
		t.Fatalf("stats included inactive or unauthorized users: %v", config.Experimental.V2RayAPI.Stats.Users)
	}
}

func TestGenerateConfigDoesNotFallbackToLegacyAccessOnLookupFailure(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createAccessNode(t, "shared")
	user, err := env.users.Create(model.CreateUserReq{Name: "legacy-only"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.users.DB.Exec(`INSERT INTO user_access (user_id, node_id) VALUES (?, ?)`, user.ID, node.ID); err != nil {
		t.Fatal(err)
	}
	h := &ConfigHandler{Users: env.users, Nodes: env.nodes, Access: env.access}
	data, err := h.generateConfig(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Inbounds []json.RawMessage `json:"inbounds"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Inbounds) != 0 {
		t.Fatalf("legacy grant enabled listener: %s", data)
	}
	if _, err := env.users.DB.Exec(`DROP TABLE user_inbound_access`); err != nil {
		t.Fatal(err)
	}
	if data, err := h.generateConfig(node.ID); err == nil || len(data) != 0 {
		t.Fatalf("authorization lookup failed open: data=%s err=%v", data, err)
	}
}

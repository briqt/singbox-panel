package singbox

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/briqt/singbox-panel/model"
)

func egressFixture() ([]model.NodeInbound, map[int][]model.User) {
	settings := json.RawMessage(`{"sni":"www.example.com","private_key":"private","public_key":"public","short_id":"abcd"}`)
	inbounds := []model.NodeInbound{
		{ID: 1, Tag: "vless-reality", Protocol: "vless-reality", Port: 8443, Enabled: true, Settings: settings},
		{ID: 2, Tag: "reality-upstream", Name: "东京-上游", Protocol: "vless-reality", Port: 2053, Enabled: true, Custom: true, Settings: settings,
			Outbound: &model.OutboundConfig{Type: "socks", Server: "192.0.2.1", ServerPort: 443, Username: "upstream-user", Password: "upstream-secret"}},
		{ID: 3, Tag: "not-authorized", Protocol: "vless-reality", Port: 2083, Enabled: true, Settings: settings},
	}
	users := map[int][]model.User{1: {{ID: 1, Name: "direct-user", UUID: "direct-uuid"}}, 2: {{ID: 2, Name: "upstream-user", UUID: "upstream-uuid"}}}
	return inbounds, users
}

func decodeGenerated(t *testing.T, data []byte, err error) map[string]any {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func TestInboundAuthorizationAndExclusiveUpstreamRoute(t *testing.T) {
	inbounds, users := egressFixture()
	data, err := GenerateConfigForInbounds(users, inbounds)
	config := decodeGenerated(t, data, err)
	actual := config["inbounds"].([]any)
	if len(actual) != 2 {
		t.Fatalf("未授权入站不应监听，实际条数 %d", len(actual))
	}
	for index, expected := range []string{"direct-uuid", "upstream-uuid"} {
		auth := actual[index].(map[string]any)["users"].([]any)
		if len(auth) != 1 || auth[0].(map[string]any)["uuid"] != expected {
			t.Fatalf("第 %d 条授权串线：%v", index, auth)
		}
	}
	outbounds := config["outbounds"].([]any)
	if len(outbounds) != 2 || outbounds[0].(map[string]any)["type"] != "direct" {
		t.Fatalf("应保留原直连出口：%v", outbounds)
	}
	upstream := outbounds[1].(map[string]any)
	for key, expected := range map[string]any{"type": "socks", "version": "5", "server": "192.0.2.1", "server_port": float64(443), "username": "upstream-user", "password": "upstream-secret", "network": "tcp"} {
		if upstream[key] != expected {
			t.Fatalf("出口字段 %s=%v，期望 %v", key, upstream[key], expected)
		}
	}
	route := config["route"].(map[string]any)
	rules := route["rules"].([]any)
	if route["final"] != "direct" || len(rules) != 1 {
		t.Fatalf("默认直连不应改变，且仅新增一条规则：%v", route)
	}
	rule := rules[0].(map[string]any)
	if !reflect.DeepEqual(rule["inbound"], []any{"reality-upstream"}) || rule["outbound"] != upstream["tag"] || rule["action"] != "route" {
		t.Fatalf("新线路必须固定走上游，不应存在选择器或直连回退：%v", rule)
	}
}

func TestUpstreamBindingSurvivesReorderingAndTagCollisions(t *testing.T) {
	inbounds, users := egressFixture()
	inbounds[0].Tag = "shared"
	inbounds[1].Tag = "shared"
	for _, ordered := range [][]model.NodeInbound{inbounds[:2], {inbounds[1], inbounds[0]}} {
		data, err := GenerateConfigForInbounds(users, ordered)
		config := decodeGenerated(t, data, err)
		var upstreamInboundTag any
		seen := map[string]bool{}
		for _, item := range config["inbounds"].([]any) {
			ib := item.(map[string]any)
			tag := ib["tag"].(string)
			if seen[tag] {
				t.Fatalf("重复 tag %q", tag)
			}
			seen[tag] = true
			if ib["listen_port"] == float64(2053) {
				upstreamInboundTag = tag
			}
		}
		rule := config["route"].(map[string]any)["rules"].([]any)[0].(map[string]any)
		if !reflect.DeepEqual(rule["inbound"], []any{upstreamInboundTag}) {
			t.Fatalf("重排后出口串线：%v", rule)
		}
	}
	used := map[string]bool{"shared": true, "shared-2053": true, "shared-2053-1": true}
	if got := uniqueTag("shared", "vless-reality", 2053, used); got != "shared-2053-2" {
		t.Fatalf("未继续处理后缀碰撞：%q", got)
	}
}

func TestNewLinePreservesOldInboundAndSubscription(t *testing.T) {
	inbounds, users := egressFixture()
	beforeBytes, err := GenerateConfigForInbounds(users, inbounds[:1])
	before := decodeGenerated(t, beforeBytes, err)
	afterBytes, err := GenerateConfigForInbounds(users, inbounds[:2])
	after := decodeGenerated(t, afterBytes, err)
	if !reflect.DeepEqual(before["inbounds"].([]any)[0], after["inbounds"].([]any)[0]) {
		t.Fatal("新增线路改动了原入站")
	}
	if _, exists := before["route"]; exists {
		t.Fatal("旧直出配置不应新增 route")
	}
	node := model.Node{Name: "tokyo", Host: "node.example.com", Enabled: true}
	user := users[1][0]
	oldSub := GenerateSubscription(user, []model.NodeWithInbounds{{Node: node, Inbounds: inbounds[:1]}})
	newSub := GenerateSubscription(user, []model.NodeWithInbounds{{Node: node, Inbounds: inbounds[:2]}})
	lines := strings.Split(strings.TrimSpace(newSub), "\n")
	if len(lines) != 2 || lines[0]+"\n" != oldSub {
		t.Fatal("原订阅条目不应改名或改参数")
	}
	uri, err := url.Parse(lines[1])
	if err != nil || uri.Fragment != inbounds[1].Name || uri.Port() != "2053" || uri.Query().Get("type") != "tcp" {
		t.Fatalf("新线路参数错误：%v %v", uri, err)
	}
	clash := GenerateClashConfig(user, []model.NodeWithInbounds{{Node: node, Inbounds: inbounds[:2]}})
	if !hasYAMLLine(clash, "udp", "false") {
		t.Fatal("TCP 上游不可宣称支持 UDP")
	}
	if got := autoProxyNames(clash); !reflect.DeepEqual(got, []string{"tokyo-Reality"}) {
		t.Fatalf("新增上游线不应改变 Auto 组：%v", got)
	}
	for _, output := range []string{newSub, clash} {
		if strings.Contains(output, "upstream-secret") || strings.Contains(output, "192.0.2.1") {
			t.Fatal("订阅泄漏服务端上游信息")
		}
	}
}

func autoProxyNames(config string) []string {
	var names []string
	inAuto := false
	for _, line := range strings.Split(config, "\n") {
		if line == "  - name: Auto" {
			inAuto = true
			continue
		}
		if inAuto && strings.HasPrefix(line, "      - ") {
			value := strings.TrimPrefix(line, "      - ")
			if parsed, err := strconv.Unquote(value); err == nil {
				value = parsed
			}
			names = append(names, value)
		} else if inAuto && (strings.HasPrefix(line, "rules:") || strings.HasPrefix(line, "  - name:")) {
			break
		}
	}
	return names
}

func TestInvalidUpstreamCannotSilentlyBecomeDirect(t *testing.T) {
	inbounds, users := egressFixture()
	inbounds[1].Outbound.Type = "unsupported"
	if _, err := GenerateConfigForInbounds(users, inbounds); err == nil {
		t.Fatal("损坏的出口配置必须报错，不可忽略后变成直出")
	}
}

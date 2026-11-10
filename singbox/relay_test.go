package singbox

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/briqt/singbox-panel/model"
)

const testRelayKey = "AAECAwQFBgcICQoLDA0ODw==" // 16 字节，2022-blake3-aes-128-gcm

func relayInbound(sources ...string) model.NodeInbound {
	settings, _ := json.Marshal(map[string]any{"method": model.DefaultSS2022Method, "password": testRelayKey, "allowed_sources": sources})
	return model.NodeInbound{ID: 9, Tag: "relay", Protocol: RelayProtocol, Port: 8443, Enabled: true, Custom: true, Settings: settings}
}

func ssUpstreamFixture(network string) model.NodeInbound {
	settings := json.RawMessage(`{"sni":"www.example.com","private_key":"private","public_key":"public","short_id":"abcd"}`)
	return model.NodeInbound{ID: 2, Tag: "vless-reality-laxcg", Name: "东京-Reality-laxcg", Protocol: "vless-reality", Port: 2053, Enabled: true, Custom: true, Settings: settings,
		Outbound: &model.OutboundConfig{Type: "shadowsocks", Server: "198.51.100.7", ServerPort: 8443, Method: model.DefaultSS2022Method, Password: testRelayKey, Network: network}}
}

func generatedRules(t *testing.T, config map[string]any) []map[string]any {
	t.Helper()
	route, _ := config["route"].(map[string]any)
	if route == nil {
		t.Fatal("配置缺少 route")
	}
	if route["final"] != "direct" {
		t.Fatalf("final 应保持 direct：%v", route["final"])
	}
	var rules []map[string]any
	for _, rule := range route["rules"].([]any) {
		rules = append(rules, rule.(map[string]any))
	}
	return rules
}

// 落地入站没有用户也必须监听；白名单之外的来源要被显式 reject，
// 因为 final 是 direct——只写放行规则等于不设白名单。
func TestRelayInboundListensWithoutUsersAndRejectsOtherSources(t *testing.T) {
	data, err := GenerateConfigForInbounds(map[int][]model.User{}, []model.NodeInbound{relayInbound("203.0.113.4", "198.51.100.0/24")})
	config := decodeGenerated(t, data, err)
	inbounds := config["inbounds"].([]any)
	if len(inbounds) != 1 {
		t.Fatalf("落地入站不应依赖用户授权：%v", inbounds)
	}
	inbound := inbounds[0].(map[string]any)
	for key, want := range map[string]any{"type": "shadowsocks", "method": model.DefaultSS2022Method, "password": testRelayKey, "listen_port": float64(8443)} {
		if inbound[key] != want {
			t.Fatalf("落地入站 %s=%v，期望 %v", key, inbound[key], want)
		}
	}
	if _, hasUsers := inbound["users"]; hasUsers {
		t.Fatal("落地入站不应携带用户列表")
	}
	rules := generatedRules(t, config)
	if len(rules) != 2 {
		t.Fatalf("应恰好有放行与拒绝两条规则：%v", rules)
	}
	allow, deny := rules[0], rules[1]
	if !reflect.DeepEqual(allow["source_ip_cidr"], []any{"203.0.113.4/32", "198.51.100.0/24"}) || allow["outbound"] != "direct" || allow["action"] != "route" {
		t.Fatalf("放行规则不对：%v", allow)
	}
	if deny["action"] != "reject" || !reflect.DeepEqual(deny["inbound"], []any{"relay"}) || deny["source_ip_cidr"] != nil {
		t.Fatalf("白名单之外必须 reject：%v", deny)
	}
}

func TestRelayRefusesUnsafeSettings(t *testing.T) {
	cases := map[string]model.NodeInbound{
		"空白名单":   relayInbound(),
		"全网白名单":  relayInbound("0.0.0.0/0"),
		"非地址条目":  relayInbound("laxcg.example.com"),
		"密钥长度不对": func() model.NodeInbound {
			ib := relayInbound("203.0.113.4")
			ib.Settings, _ = json.Marshal(map[string]any{"method": model.DefaultSS2022Method, "password": base64.StdEncoding.EncodeToString(make([]byte, 32)), "allowed_sources": []string{"203.0.113.4"}})
			return ib
		}(),
		"串接上游": func() model.NodeInbound {
			ib := relayInbound("203.0.113.4")
			ib.Outbound = &model.OutboundConfig{Type: "socks", Server: "192.0.2.1", ServerPort: 1080}
			return ib
		}(),
	}
	for name, ib := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := GenerateConfigForInbounds(nil, []model.NodeInbound{ib}); err == nil {
				t.Fatal("不安全的落地配置必须拒绝生成")
			}
		})
	}
}

// 入口线路的全部流量只能走 SS 出口：路由里该入站只有一条规则，且指向专属出口。
func TestShadowsocksUpstreamIsTheOnlyExitForItsInbound(t *testing.T) {
	for _, network := range []string{"", "tcp"} {
		t.Run("network="+network, func(t *testing.T) {
			ib := ssUpstreamFixture(network)
			data, err := GenerateConfigForInbounds(map[int][]model.User{2: {{ID: 1, Name: "u", UUID: "uuid-1"}}}, []model.NodeInbound{ib})
			config := decodeGenerated(t, data, err)
			outbounds := config["outbounds"].([]any)
			if len(outbounds) != 2 {
				t.Fatalf("应只有 direct 与专属出口：%v", outbounds)
			}
			upstream := outbounds[1].(map[string]any)
			for key, want := range map[string]any{"type": "shadowsocks", "tag": "upstream-vless-reality-laxcg", "server": "198.51.100.7", "server_port": float64(8443), "method": model.DefaultSS2022Method, "password": testRelayKey} {
				if upstream[key] != want {
					t.Fatalf("出口 %s=%v，期望 %v", key, upstream[key], want)
				}
			}
			gotNetwork, present := upstream["network"]
			if network == "" && present {
				t.Fatalf("未限定网络时不应写 network（否则 UDP 被截断）：%v", gotNetwork)
			}
			if network == "tcp" && gotNetwork != "tcp" {
				t.Fatalf("限定 TCP 时必须写入：%v", gotNetwork)
			}
			rules := generatedRules(t, config)
			if len(rules) != 1 || !reflect.DeepEqual(rules[0]["inbound"], []any{"vless-reality-laxcg"}) || rules[0]["outbound"] != "upstream-vless-reality-laxcg" {
				t.Fatalf("入站必须只绑定专属出口：%v", rules)
			}
		})
	}
}

func TestBrokenShadowsocksUpstreamCannotBecomeDirect(t *testing.T) {
	for name, mutate := range map[string]func(*model.OutboundConfig){
		"旧式方法":   func(o *model.OutboundConfig) { o.Method = "aes-128-gcm" },
		"密钥损坏":   func(o *model.OutboundConfig) { o.Password = "not-base64!" },
		"仅UDP网络": func(o *model.OutboundConfig) { o.Network = "udp" },
		"缺少服务器":  func(o *model.OutboundConfig) { o.Server = "" },
	} {
		t.Run(name, func(t *testing.T) {
			ib := ssUpstreamFixture("")
			mutate(ib.Outbound)
			if _, err := GenerateConfigForInbounds(map[int][]model.User{2: {{ID: 1, Name: "u", UUID: "uuid-1"}}}, []model.NodeInbound{ib}); err == nil {
				t.Fatal("损坏的 SS 出口必须报错，不可忽略后变成直出")
			}
		})
	}
}

// 落地入站不发给任何用户：即便误授权，订阅里也不能出现它或它的密钥。
func TestRelayNeverReachesSubscriptions(t *testing.T) {
	user := model.User{ID: 1, Name: "u", UUID: "uuid-1"}
	node := model.NodeWithInbounds{Node: model.Node{Name: "laxcg", Host: "198.51.100.7", Enabled: true}, Inbounds: []model.NodeInbound{relayInbound("203.0.113.4")}}
	entry := model.NodeWithInbounds{Node: model.Node{Name: "tokyo", Host: "203.0.113.4", Enabled: true}, Inbounds: []model.NodeInbound{ssUpstreamFixture("")}}
	uris := GenerateSubscription(user, []model.NodeWithInbounds{node, entry})
	clash := GenerateClashConfig(user, []model.NodeWithInbounds{node, entry})
	for format, doc := range map[string]string{"uri": uris, "clash": clash} {
		if strings.Contains(doc, testRelayKey) {
			t.Fatalf("%s 订阅泄露了落地密钥", format)
		}
		if strings.Contains(doc, ":8443") || strings.Contains(doc, "shadowsocks") || strings.Contains(doc, "type: ss") {
			t.Fatalf("%s 订阅出现了落地入站：%s", format, doc)
		}
	}
	if lines := strings.Split(strings.TrimSpace(uris), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], "vless://") {
		t.Fatalf("只应有入口线路一条：%v", lines)
	}
}

// Clash 的 udp 开关必须如实反映出口能否承载 UDP；上游线路仍不进 Auto。
func TestClashUDPFlagFollowsUpstreamCapability(t *testing.T) {
	user := model.User{ID: 1, Name: "u", UUID: "uuid-1"}
	socks := ssUpstreamFixture("")
	socks.ID, socks.Name, socks.Port = 3, "socks-line", 2083
	socks.Outbound = &model.OutboundConfig{Type: "socks", Server: "192.0.2.1", ServerPort: 1080, Network: "tcp"}
	ssTCP := ssUpstreamFixture("tcp")
	ssTCP.ID, ssTCP.Name, ssTCP.Port = 4, "ss-tcp-line", 2087
	node := model.NodeWithInbounds{Node: model.Node{Name: "tokyo", Host: "203.0.113.4", Enabled: true}, Inbounds: []model.NodeInbound{ssUpstreamFixture(""), socks, ssTCP}}
	clash := GenerateClashConfig(user, []model.NodeWithInbounds{node})
	for name, want := range map[string]string{"东京-Reality-laxcg": "true", "socks-line": "false", "ss-tcp-line": "false"} {
		if got := clashProxyField(clash, name, "udp"); got != want {
			t.Fatalf("%s udp=%q，期望 %q", name, got, want)
		}
	}
	// 只有上游线路时 Auto 退化为仅含 REJECT 的手选组，绝不回落 DIRECT。
	if names := autoProxyNames(clash); !reflect.DeepEqual(names, []string{"REJECT"}) {
		t.Fatalf("上游线路不应进入 Auto：%v", names)
	}
}

// clashProxyField 在指定代理块内按整行取值，避免一个代理的字段被误读成另一个的。
func clashProxyField(doc, proxy, key string) string {
	inBlock := false
	for _, line := range strings.Split(doc, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- name: ") {
			inBlock = trimmed == `- name: "`+proxy+`"`
			continue
		}
		if inBlock {
			if k, v, ok := strings.Cut(trimmed, ": "); ok && k == key {
				return v
			}
		}
	}
	return ""
}

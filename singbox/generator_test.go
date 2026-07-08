package singbox

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/briqt/singbox-panel/model"
)

func TestHysteria2UsesAdaptiveCongestionControlByDefault(t *testing.T) {
	configBytes, err := GenerateConfig([]model.User{{Name: "test", UUID: "uuid"}}, []model.NodeInbound{{
		Tag: "hy2", Protocol: "hysteria2", Port: 24443, Enabled: true,
		Settings: json.RawMessage(`{"domain":"node.example.com","cert_path":"/cert","key_path":"/key"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Inbounds []map[string]any `json:"inbounds"`
	}
	if err := json.Unmarshal(configBytes, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Inbounds) != 1 {
		t.Fatalf("inbounds=%#v", config.Inbounds)
	}
	if _, exists := config.Inbounds[0]["up_mbps"]; exists {
		t.Fatal("Hysteria2 up_mbps must not be hard-coded")
	}
	if _, exists := config.Inbounds[0]["down_mbps"]; exists {
		t.Fatal("Hysteria2 down_mbps must not be hard-coded")
	}
}

func TestGenerateConfigEnablesPerUserV2RayStats(t *testing.T) {
	users := []model.User{{Name: "alice", UUID: "u1"}, {Name: "bob", UUID: "u2"}}
	inbounds := []model.NodeInbound{
		{Tag: "hy2", Protocol: "hysteria2", Port: 24443, Enabled: true,
			Settings: json.RawMessage(`{"domain":"n.example.com","cert_path":"/c","key_path":"/k"}`)},
		{Tag: "reality", Protocol: "vless-reality", Port: 443, Enabled: true,
			Settings: json.RawMessage(`{"sni":"a.com","private_key":"pk","short_id":"ab"}`)},
	}
	configBytes, err := GenerateConfig(users, inbounds)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Experimental struct {
			V2RayAPI struct {
				Listen string `json:"listen"`
				Stats  struct {
					Enabled  bool     `json:"enabled"`
					Inbounds []string `json:"inbounds"`
					Users    []string `json:"users"`
				} `json:"stats"`
			} `json:"v2ray_api"`
		} `json:"experimental"`
	}
	if err := json.Unmarshal(configBytes, &config); err != nil {
		t.Fatal(err)
	}
	stats := config.Experimental.V2RayAPI.Stats
	if !stats.Enabled {
		t.Fatal("v2ray_api stats must be enabled for per-user accounting")
	}
	if config.Experimental.V2RayAPI.Listen != V2RayAPIListen {
		t.Fatalf("listen=%q want %q", config.Experimental.V2RayAPI.Listen, V2RayAPIListen)
	}
	if len(stats.Users) != 2 {
		t.Fatalf("stats users must list every user, got %#v", stats.Users)
	}
	if len(stats.Inbounds) != 2 {
		t.Fatalf("stats inbounds must list every inbound tag, got %#v", stats.Inbounds)
	}
}

func TestGenerateConfigOmitsV2RayStatsWithoutUsers(t *testing.T) {
	configBytes, err := GenerateConfig(nil, []model.NodeInbound{{
		Tag: "hy2", Protocol: "hysteria2", Port: 24443, Enabled: true,
		Settings: json.RawMessage(`{"domain":"n.example.com","cert_path":"/c","key_path":"/k"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configBytes), "v2ray_api") {
		t.Fatal("v2ray_api must be omitted when no users are assigned")
	}
}

func TestClashConfigIncludesPrivateAndChinaDirectRules(t *testing.T) {
	config := GenerateClashConfig(model.User{}, nil)
	for _, rule := range []string{
		"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
		"IP-CIDR6,fc00::/7,DIRECT,no-resolve",
		"GEOIP,CN,DIRECT,no-resolve",
	} {
		if !strings.Contains(config, rule) {
			t.Fatalf("missing rule %q", rule)
		}
	}
}

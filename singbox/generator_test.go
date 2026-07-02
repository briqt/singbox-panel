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

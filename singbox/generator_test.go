package singbox

import (
	"encoding/json"
	"net/url"
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

const hy2WithObfs = `{"domain":"node.example.com","cert_path":"/cert","key_path":"/key","obfs_password":"a1b2c3d4e5f60718"}`

func hysteria2Inbound(t *testing.T, settings string) map[string]any {
	t.Helper()
	configBytes, err := GenerateConfig([]model.User{{Name: "test", UUID: "uuid"}}, []model.NodeInbound{{
		Tag: "hy2", Protocol: "hysteria2", Port: 443, Enabled: true,
		Settings: json.RawMessage(settings),
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
	return config.Inbounds[0]
}

func TestHysteria2EnablesSalamanderObfsWhenAPasswordIsStored(t *testing.T) {
	obfs, ok := hysteria2Inbound(t, hy2WithObfs)["obfs"].(map[string]any)
	if !ok {
		t.Fatal("obfs block missing from the Hysteria2 inbound")
	}
	if obfs["type"] != "salamander" {
		t.Fatalf("obfs type=%v want salamander", obfs["type"])
	}
	if obfs["password"] != "a1b2c3d4e5f60718" {
		t.Fatalf("obfs password=%v", obfs["password"])
	}
}

// Nodes that predate the obfs rollout have no stored password. They must keep
// generating a valid config rather than an inbound with an empty obfuscator,
// which would refuse every client until the node is re-provisioned.
func TestHysteria2OmitsObfsWhenNoPasswordIsStored(t *testing.T) {
	inbound := hysteria2Inbound(t, `{"domain":"node.example.com","cert_path":"/cert","key_path":"/key"}`)
	if _, exists := inbound["obfs"]; exists {
		t.Fatal("obfs must be omitted when no password has been provisioned")
	}
}

func TestHysteria2CommandsClientsOntoBBR(t *testing.T) {
	if got := hysteria2Inbound(t, hy2WithObfs)["ignore_client_bandwidth"]; got != true {
		t.Fatalf("ignore_client_bandwidth=%v; without it clients use Brutal, which answers QoS loss by sending harder", got)
	}
}

// The failure this guards against is silent and looks exactly like a dead
// server: a client whose obfuscator password disagrees with the server's gets
// a connection timeout, with nothing in either log saying why. Server config
// and subscription are generated from the same stored settings, so assert they
// actually agree rather than assuming it.
func TestHysteria2ObfsPasswordReachesEveryClientFormat(t *testing.T) {
	inbound := model.NodeInbound{
		Tag: "hy2", Protocol: "hysteria2", Port: 443, Enabled: true,
		Settings: json.RawMessage(hy2WithObfs),
	}
	user := model.User{Name: "test", UUID: "uuid-1"}
	nodes := []model.NodeWithInbounds{{
		Node:     model.Node{Name: "tokyo", Host: "203.0.113.1", Domain: "node.example.com", Enabled: true},
		Inbounds: []model.NodeInbound{inbound},
	}}

	serverObfs, _ := hysteria2Inbound(t, hy2WithObfs)["obfs"].(map[string]any)["password"].(string)

	// Compare exact values, never substrings: "obfs-password=<pw>" is a prefix
	// of "obfs-password=<pw>x", so a Contains check silently accepts a
	// corrupted password — which is precisely the bug this test exists to catch.
	sub := strings.TrimSpace(GenerateSubscription(user, nodes))
	uri, err := url.Parse(sub)
	if err != nil {
		t.Fatalf("subscription is not a parseable URI: %v (%s)", err, sub)
	}
	query := uri.Query()
	if query.Get("obfs") != "salamander" {
		t.Fatalf("subscription URI obfs=%q want salamander", query.Get("obfs"))
	}
	if query.Get("obfs-password") != serverObfs {
		t.Fatalf("subscription URI password %q disagrees with the server's %q", query.Get("obfs-password"), serverObfs)
	}

	clash := GenerateClashConfig(user, nodes)
	if !hasYAMLLine(clash, "obfs", "salamander") {
		t.Fatalf("clash profile does not select the obfuscator: %s", clash)
	}
	if !hasYAMLLine(clash, "obfs-password", serverObfs) {
		t.Fatalf("clash profile password disagrees with the server's %q: %s", serverObfs, clash)
	}
}

// hasYAMLLine matches a whole "key: value" line so a longer value cannot pass
// as the expected one.
func hasYAMLLine(doc, key, value string) bool {
	for _, line := range strings.Split(doc, "\n") {
		k, v, found := strings.Cut(strings.TrimSpace(line), ": ")
		if found && k == key && v == value {
			return true
		}
	}
	return false
}

package singbox

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/briqt/singbox-panel/model"
)

func TestQUICProfileServesEmbeddedSiteAndIgnoresDormantObfs(t *testing.T) {
	settings := `{"profile":"quic","domain":"node.example.com","cert_path":"/c","key_path":"/k","obfs_password":"old-password"}`
	inbound := hysteria2Inbound(t, settings)
	if _, present := inbound["obfs"]; present {
		t.Fatal("QUIC profile retained an obfuscator; a browser cannot reach the site")
	}
	site := inbound["masquerade"].(map[string]any)
	if site["type"] != "string" || site["status_code"] != float64(200) || site["content"] != siteHTML {
		t.Fatalf("embedded site not served: %v", site)
	}
	node := model.Node{Host: "203.0.113.2", Domain: "node.example.com"}
	ib := model.NodeInbound{Protocol: "hysteria2", Port: 8443, Settings: json.RawMessage(settings)}
	uri, err := url.Parse(buildURI(model.User{UUID: "u"}, node, ib, protocolNameTracker{}))
	if err != nil {
		t.Fatal(err)
	}
	if uri.Hostname() != node.Domain || uri.Query().Get("obfs") != "" || uri.Query().Get("obfs-password") != "" {
		t.Fatalf("URI does not follow QUIC/domain settings: %s", uri)
	}
	clash := buildClashProxy(model.User{UUID: "u"}, node, ib, "test")
	if !hasYAMLLine(clash, "server", node.Domain) || strings.Contains(clash, "obfs:") || strings.Contains(clash, node.Host) {
		t.Fatalf("Clash does not follow QUIC/domain settings: %s", clash)
	}
}

func TestRealityDomainAddressIsSeparateFromCamouflageSNI(t *testing.T) {
	node := model.Node{Host: "203.0.113.2", Domain: "cdn.example.com"}
	ib := model.NodeInbound{Protocol: "vless-reality", Port: 8443, Settings: json.RawMessage(
		`{"server":"direct.example.com","sni":"www.example.com","public_key":"pub","short_id":"ab"}`)}
	uri, err := url.Parse(buildURI(model.User{UUID: "u"}, node, ib, protocolNameTracker{}))
	if err != nil {
		t.Fatal(err)
	}
	if uri.Hostname() != "direct.example.com" || uri.Query().Get("sni") != "www.example.com" {
		t.Fatalf("server and SNI were confused: %s", uri)
	}
	clash := buildClashProxy(model.User{UUID: "u"}, node, ib, "test")
	if !hasYAMLLine(clash, "server", "direct.example.com") || !hasYAMLLine(clash, "servername", "www.example.com") {
		t.Fatalf("Clash server and SNI were confused: %s", clash)
	}
	// A CDN domain cannot carry Reality. A legacy inbound without a verified
	// direct address must keep its original destination rather than use the CDN.
	ib.Settings = json.RawMessage(`{"sni":"www.example.com","public_key":"pub"}`)
	uri, _ = url.Parse(buildURI(model.User{UUID: "u"}, node, ib, protocolNameTracker{}))
	if uri.Hostname() != node.Host {
		t.Fatalf("legacy Reality was sent to CDN: %s", uri)
	}
}

func TestSalamanderDoesNotAdvertiseAnUnreachableWebsite(t *testing.T) {
	if _, exists := hysteria2Inbound(t, hy2WithObfs)["masquerade"]; exists {
		t.Fatal("Salamander cannot serve normal browser QUIC")
	}
}

package handler

import (
	"strings"
	"testing"
)

func TestParseListeningSockets(t *testing.T) {
	sockets := parseListeningSockets(`udp UNCONN 0 0 *:19566 *:*
tcp LISTEN 0 4096 [::]:27495 [::]:*
tcp LISTEN 0 4096 127.0.0.1:9090 0.0.0.0:*`)
	if !sockets["udp"][19566] {
		t.Fatal("missing UDP listener")
	}
	if !sockets["tcp"][27495] || !sockets["tcp"][9090] {
		t.Fatal("missing TCP listener")
	}
	if sockets["tcp"][19566] || sockets["udp"][27495] {
		t.Fatal("listener network was classified incorrectly")
	}
}

func TestInstallScriptCreatesBinDirectory(t *testing.T) {
	// The path that first broke laxcc: parent dir does not exist on a clean host.
	legacy := "/etc/v2ray-agent/sing-box/sing-box"
	script := installBinaryScript("https://example.invalid/sb.tar.gz", legacy)
	want := `mkdir -p "$(dirname "` + legacy + `")"`
	if !strings.Contains(script, want) {
		t.Fatalf("install script does not create the binary directory:\n%s", script)
	}
	if !strings.Contains(script, `install -m 0755 "$BIN" "`+legacy+`.new"`) {
		t.Fatalf("install script does not stage next to the target:\n%s", script)
	}
}

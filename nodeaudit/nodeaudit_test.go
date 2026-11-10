package nodeaudit

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/briqt/singbox-panel/model"
)

func TestDesiredForLabelsInboundsAndSSH(t *testing.T) {
	node := &model.Node{Port: 39215}
	d := DesiredFor(node, []model.NodeInbound{
		{Tag: "vless-reality", Protocol: "vless-reality", Port: 443, Enabled: true},
		{Tag: "hysteria2", Protocol: "hysteria2", Port: 443, Enabled: true},
		{Tag: "old", Protocol: "vless-reality", Port: 2053, Enabled: false},
	}, "")
	got := map[string]string{}
	for _, p := range d.Ports {
		got[p.Proto+"/"+itoa(p.Port)] = p.Label
	}
	want := map[string]string{"tcp/443": "vless_reality", "udp/443": "hysteria2", "tcp/39215": "ssh"}
	if len(got) != len(want) {
		t.Fatalf("ports = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("port %s label = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if d.Units[0] != "sing-box.service" {
		t.Fatalf("units = %v", d.Units)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestPreflightBlockers(t *testing.T) {
	ok := Preflight{Python: "3.9", NFT: "yes", SS: "yes", IP: "yes", Cgroup: "cgroup2fs", FreeBytes: 5 << 30}
	if b, w := ok.Problems(); len(b) != 0 || len(w) != 0 {
		t.Fatalf("healthy node flagged: %v %v", b, w)
	}
	for name, mutate := range map[string]func(*Preflight){
		"old python": func(p *Preflight) { p.Python = "3.8" },
		"no python":  func(p *Preflight) { p.Python = "none" },
		"no nft":     func(p *Preflight) { p.NFT = "no" },
		"no ss":      func(p *Preflight) { p.SS = "no" },
		"full disk":  func(p *Preflight) { p.FreeBytes = 900 << 20 },
	} {
		p := ok
		mutate(&p)
		if b, _ := p.Problems(); len(b) == 0 {
			t.Fatalf("%s: install was not blocked", name)
		}
	}
	p := ok
	p.Cgroup = "tmpfs"
	if b, w := p.Problems(); len(b) != 0 || len(w) != 1 {
		t.Fatalf("cgroup v1 should warn, not block: %v %v", b, w)
	}
}

// TestHealthyRejectsWhatItGuards is the falsification test for the install
// read-back: each failure mode it exists to catch must actually fail it.
func TestHealthyRejectsWhatItGuards(t *testing.T) {
	good := Status{Installed: true, Timer: "active", LastSample: 1, LastSampleAge: 30}
	if ok, why := good.Healthy(); !ok {
		t.Fatalf("healthy status rejected: %s", why)
	}
	for name, s := range map[string]Status{
		"timer inactive": {Installed: true, Timer: "inactive", LastSample: 1, LastSampleAge: 30},
		"stale sample":   {Installed: true, Timer: "active", LastSample: 1, LastSampleAge: 600},
		"never sampled":  {Installed: true, Timer: "active"},
		"pending error":  {Installed: true, Timer: "active", LastSample: 1, LastSampleAge: 30, LastError: json.RawMessage(`{"error":"RuntimeError"}`)},
		"not installed":  {},
	} {
		if ok, _ := s.Healthy(); ok {
			t.Fatalf("%s: reported healthy", name)
		}
	}
}

func TestEmbeddedFilesAreTheCollector(t *testing.T) {
	if !strings.Contains(string(Script), "VERSION = '2'") {
		t.Fatal("embedded script is not the v2 collector")
	}
	for _, want := range []string{"CPUQuota=10%", "MemoryMax=96M", "StandardError=journal", "ProtectSystem=strict"} {
		if !strings.Contains(string(ServiceUnit), want) {
			t.Fatalf("service unit lacks %q", want)
		}
	}
	if len(ScriptSHA256()) != 64 {
		t.Fatal("bad sha")
	}
}

package handler

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
)

var errProbeFailed = errors.New("ssh: probe failed")

func TestAutoSetupRejectsInvalidRequestsBeforeSSH(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "node-1")
	setup := &SetupHandler{
		Nodes: env.nodes,
		Config: &ConfigHandler{
			Users: env.users, Nodes: env.nodes, Access: env.access,
		},
	}
	path := "/api/nodes/" + strconv.Itoa(node.ID) + "/auto-setup"

	tests := []struct {
		name string
		body map[string]any
	}{
		{name: "duplicate protocol", body: map[string]any{"protocols": []string{"vless-reality", "vless-reality"}}},
		{name: "domain protocol without domain", body: map[string]any{"protocols": []string{"hysteria2"}}},
		{name: "unsupported protocol", body: map[string]any{"protocols": []string{"trojan"}}},
		{name: "invalid port", body: map[string]any{"protocols": []string{"vless-reality"}, "ports": map[string]any{"reality": 70000}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := performJSONRequest(t, http.HandlerFunc(setup.HandleAutoSetup), http.MethodPost, path, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestParseFastestRealityProbe(t *testing.T) {
	host, latency := parseFastestRealityProbe(
		"attacker.invalid 2 200 0.001\nwww.apple.com 2 200 0.083\ninvalid\nwww.microsoft.com 2 200 0.041\nwww.amazon.com 2 200 0.122\n")
	if host != "www.microsoft.com" || latency != 0.041 {
		t.Fatalf("host=%q latency=%v", host, latency)
	}
}

// The regression this gate exists for. updates.cdn-apple.com is a CDN edge, so
// it wins on latency by a wide margin and was picked on all three direct nodes
// — while answering HTTP/1.1 with a 403, which the site it impersonates never
// does. Latency must not be able to buy its way past the behaviour check.
func TestParseFastestRealityProbeRejectsFastCDNEdgeWithoutH2(t *testing.T) {
	host, _ := parseFastestRealityProbe(
		"updates.cdn-apple.com 1.1 403 0.005\nswcdn.apple.com 1.1 404 0.008\nwww.microsoft.com 2 200 0.190\n")
	if host != "www.microsoft.com" {
		t.Fatalf("a faster HTTP/1.1 403 edge was selected: host=%q", host)
	}
}

func TestParseFastestRealityProbeRejectsErrorStatus(t *testing.T) {
	for _, probe := range []string{
		"www.apple.com 2 403 0.010\n",
		"www.apple.com 2 500 0.010\n",
		"www.apple.com 2 404 0.010\n",
		"www.apple.com 1.1 200 0.010\n",
	} {
		if host, _ := parseFastestRealityProbe(probe); host != "" {
			t.Fatalf("probe %q was accepted as host=%q", probe, host)
		}
	}
}

func TestParseRealityProbeIgnoresHostsItDidNotAskAbout(t *testing.T) {
	host, _ := parseRealityProbe("attacker.invalid 2 200 0.001\n", map[string]bool{"www.apple.com": true})
	if host != "" {
		t.Fatalf("an unrequested host was accepted: %q", host)
	}
}

func TestSelectRealitySNIUsesSuccessfulFastestProbe(t *testing.T) {
	host, err := selectRealitySNI(func(command string) (string, error) {
		if command == "" {
			t.Fatal("probe command is empty")
		}
		return "www.apple.com 2 200 0.090\nwww.mozilla.org 2 200 0.052\n", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if host != "www.mozilla.org" {
		t.Fatalf("host=%q", host)
	}
}

func TestSelectRealitySNIFailsWhenNoCandidateQualifies(t *testing.T) {
	_, err := selectRealitySNI(func(string) (string, error) {
		return "www.apple.com 1.1 403 0.010\nwww.mozilla.org 1.1 403 0.020\n", nil
	})
	if err == nil {
		t.Fatal("expected an error when every candidate fails the behaviour check")
	}
}

func TestRealityDestStillQualifies(t *testing.T) {
	tests := []struct {
		name  string
		out   string
		err   error
		keeps bool
	}{
		{name: "healthy target is kept", out: "www.microsoft.com 2 200 0.040\n", keeps: true},
		{name: "target that lost h2 is rejected", out: "www.microsoft.com 1.1 200 0.040\n"},
		{name: "target that started erroring is rejected", out: "www.microsoft.com 2 403 0.040\n"},
		{name: "probe failure keeps the target", err: errProbeFailed, keeps: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := realityDestStillQualifies(func(string) (string, error) {
				return tt.out, tt.err
			}, "www.microsoft.com")
			if got != tt.keeps {
				t.Fatalf("qualifies=%v want %v", got, tt.keeps)
			}
		})
	}
}

// ss output shapes, kept verbatim so the parser is tested against the thing it
// actually reads rather than an idealised version of it.
const ssTCPWithCaddyOn443 = `State  Recv-Q Send-Q Local Address:Port  Peer Address:Port Process
LISTEN 0      4096         0.0.0.0:22         0.0.0.0:*         users:(("sshd",pid=1,fd=3))
LISTEN 0      4096               *:443              *:*         users:(("caddy",pid=674287,fd=40))
LISTEN 0      4096               *:80               *:*         users:(("caddy",pid=674287,fd=42))
LISTEN 0      4096            [::]:31795         [::]:*         users:(("sing-box",pid=999,fd=8))
`

const ssTCPBare = `State  Recv-Q Send-Q Local Address:Port  Peer Address:Port
LISTEN 0      4096         0.0.0.0:22         0.0.0.0:*
`

func TestParseListeningPortsReadsRealSSOutput(t *testing.T) {
	ports := parseListeningPorts(ssTCPWithCaddyOn443)
	for _, want := range []int{22, 443, 80, 31795} {
		if !ports[want] {
			t.Fatalf("port %d not detected in ss output", want)
		}
	}
	if ports[8443] {
		t.Fatal("8443 reported as occupied when nothing listens on it")
	}
}

// tokyo's exact situation: Caddy owns 443 for panel/hs/derp/fn, so Reality has
// to land on the next conventional port rather than stealing the web stack's.
func TestSelectListenPortFallsBackWhenPreferredIsTaken(t *testing.T) {
	port := selectListenPort(func(string) (string, error) { return ssTCPWithCaddyOn443, nil }, "tcp", 31795)
	if port != 8443 {
		t.Fatalf("port=%d want 8443", port)
	}
}

func TestSelectListenPortPrefers443WhenFree(t *testing.T) {
	port := selectListenPort(func(string) (string, error) { return ssTCPBare, nil }, "tcp", 0)
	if port != 443 {
		t.Fatalf("port=%d want 443", port)
	}
}

// Re-running setup on an already-correct node must be a no-op. The inbound's
// own listener shows up as occupied, so without discounting it the node would
// walk one step down the preference list on every single run.
func TestSelectListenPortKeepsThePortItAlreadyOwns(t *testing.T) {
	occupied := ssTCPBare + "LISTEN 0 4096 *:443 *:* users:((\"sing-box\",pid=7,fd=9))\n"
	port := selectListenPort(func(string) (string, error) { return occupied, nil }, "tcp", 443)
	if port != 443 {
		t.Fatalf("port=%d want 443 (its own listener must not count against it)", port)
	}
}

// A probe that returns nothing parseable must not be read as "every port is
// free" — that would hand out a port already in use and the push would fail on
// the node instead of here.
func TestListeningPortsRejectsUnparseableOutput(t *testing.T) {
	if _, err := listeningPorts(func(string) (string, error) { return "command not found", nil }, "tcp"); err == nil {
		t.Fatal("expected an error when the probe yields no usable rows")
	}
}

func TestSelectListenPortFallsBackToCurrentPortWhenProbeFails(t *testing.T) {
	port := selectListenPort(func(string) (string, error) { return "", errProbeFailed }, "udp", 24307)
	if port != 24307 {
		t.Fatalf("port=%d want the port already in use", port)
	}
}

func TestListeningPortsAsksForTheRightL4Network(t *testing.T) {
	var got string
	listeningPorts(func(command string) (string, error) {
		got = command
		return ssTCPBare, nil
	}, "udp")
	if !contains(got, "-lnu") {
		t.Fatalf("udp probe did not ask for udp sockets: %q", got)
	}
}

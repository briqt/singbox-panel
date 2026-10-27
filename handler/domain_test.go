package handler

import (
	"errors"
	"testing"
)

func TestExplicitRealityTargetMustPassProbe(t *testing.T) {
	for _, tc := range []struct {
		output string
		err    error
		valid  bool
	}{
		{"www.mozilla.org 2 200 0.08\n", nil, true},
		{"www.mozilla.org 1.1 200 0.08\n", nil, false},
		{"www.mozilla.org 2 403 0.08\n", nil, false},
		{"www.samsung.com 2 200 0.08\n", nil, false},
		{"", errors.New("probe failed"), false},
	} {
		host, err := chooseRealitySNI(func(string) (string, error) { return tc.output, tc.err }, "www.samsung.com", "www.mozilla.org")
		if (err == nil) != tc.valid || (tc.valid && host != "www.mozilla.org") {
			t.Fatalf("output=%q host=%q err=%v", tc.output, host, err)
		}
	}
}

func TestHysteriaSettingsPreserveIdentityAndApplyExplicitProfile(t *testing.T) {
	old := map[string]any{"obfs_password": "unchanged", "profile": "salamander"}
	settings := hysteriaSettings(old, "", "new.example.com")
	if settings["obfs_password"] != "unchanged" || settings["profile"] != "salamander" || settings["cert_path"] != "/etc/sing-box/tls/new.example.com.crt" {
		t.Fatalf("settings=%v", settings)
	}
	quic := hysteriaSettings(old, "quic", "new.example.com")
	if quic["profile"] != "quic" || old["profile"] != "salamander" {
		t.Fatal("explicit profile ignored or old snapshot mutated")
	}
	fresh := hysteriaSettings(nil, "", "new.example.com")
	if fresh["profile"] != "quic" || fresh["obfs_password"] != nil {
		t.Fatalf("new node defaults=%v", fresh)
	}
}

func TestDirectDNSAcceptsManagementDomainAndRejectsStaleAAAA(t *testing.T) {
	lookup := staticLookup(map[string][]string{"ssh.example.com": {"203.0.113.1", "2001:db8::1"}})
	if !recordsPointToNode([]string{"203.0.113.1", "2001:db8::1"}, "ssh.example.com", lookup) {
		t.Fatal("DNS management host rejected")
	}
	if recordsPointToNode([]string{"203.0.113.1", "2001:db8::bad"}, "ssh.example.com", lookup) {
		t.Fatal("stale AAAA passed because its A record matches")
	}
	if recordsPointToNode([]string{"203.0.113.1", "198.51.100.1"}, "203.0.113.1", lookup) {
		t.Fatal("mixed destinations passed direct-DNS gate")
	}
}

package handler

import "testing"

func TestValidDomainName(t *testing.T) {
	for _, domain := range []string{"lax03.briqt.dev", "xn--fsqu00a.xn--0zwm56d"} {
		if !validDomainName(domain) {
			t.Fatalf("valid domain rejected: %q", domain)
		}
	}
	for _, domain := range []string{"", "localhost", ".example.com", "example.com.", "-bad.example", "bad-.example", "bad;touch-x.example"} {
		if validDomainName(domain) {
			t.Fatalf("invalid domain accepted: %q", domain)
		}
	}
}

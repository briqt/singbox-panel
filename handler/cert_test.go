package handler

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/briqt/singbox-panel/model"
)

// writeTestCert drops a self-signed cert/key pair on disk with the requested
// expiry and returns their paths.
func writeTestCert(t *testing.T, dir, name string, notAfter time.Time) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    notAfter.Add(-90 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPath := filepath.Join(dir, name+".crt")
	keyPath := filepath.Join(dir, name+".key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte("key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

// runRenewScript executes the real generated script under a stubbed PATH so it
// cannot reach the network. acme.sh is absent in the test environment, so any
// run that gets past the freshness gate stops at ACME_MISSING — which is
// exactly the signal we want: "the gate decided to renew".
func runRenewScript(t *testing.T, certPath, keyPath string, force bool) string {
	t.Helper()
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not available; the freshness gate is shell-side")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	stubDir := t.TempDir()
	// Shadow curl so the acme.sh bootstrap cannot fetch anything.
	if err := os.WriteFile(filepath.Join(stubDir, "curl"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write curl stub: %v", err)
	}
	script := renewCertScript("example.test", certPath, keyPath, force, certRenewBeforeDays)
	cmd := exec.Command(bash, "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// TestRenewGateRejectsExpiredCert is the falsification test for the gate this
// change introduces. The bug it replaces skipped renewal whenever the cert
// *files existed*, so a cert that expired weeks ago was treated as done and the
// node stayed broken silently. Here the expired cert must NOT be called fresh.
func TestRenewGateRejectsExpiredCert(t *testing.T) {
	dir := t.TempDir()
	expiredCert, expiredKey := writeTestCert(t, dir, "expired", time.Now().Add(-8*24*time.Hour))

	// The predicate the old code used still passes — the files are right there.
	// That is precisely why file existence is not a health check.
	if _, err := os.Stat(expiredCert); err != nil {
		t.Fatalf("expired cert should exist on disk: %v", err)
	}
	if _, err := os.Stat(expiredKey); err != nil {
		t.Fatalf("expired key should exist on disk: %v", err)
	}

	out := runRenewScript(t, expiredCert, expiredKey, false)
	if strings.Contains(out, "CERT_FRESH") {
		t.Fatalf("expired certificate was treated as fresh; the gate does not fire.\noutput:\n%s", out)
	}
	if !strings.Contains(out, "ACME_MISSING") {
		t.Fatalf("expected the script to proceed to issuance, got:\n%s", out)
	}
}

// TestRenewGateSkipsHealthyCert is the other half: the gate must not renew a
// cert that is fine, or every status poll would burn an ACME order.
func TestRenewGateSkipsHealthyCert(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTestCert(t, dir, "healthy", time.Now().Add(80*24*time.Hour))
	out := runRenewScript(t, certPath, keyPath, false)
	if !strings.Contains(out, "CERT_FRESH") {
		t.Fatalf("healthy certificate should be left alone, got:\n%s", out)
	}
}

// TestRenewGateNearExpiryRenews covers the window acme.sh's own cron should have
// handled: still valid, but inside the renewal threshold.
func TestRenewGateNearExpiryRenews(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTestCert(t, dir, "soon", time.Now().Add(10*24*time.Hour))
	out := runRenewScript(t, certPath, keyPath, false)
	if strings.Contains(out, "CERT_FRESH") {
		t.Fatalf("cert inside the %d-day threshold should renew, got:\n%s", certRenewBeforeDays, out)
	}
}

// TestRenewForceOverridesFreshness makes sure an operator can re-issue a cert
// that the gate would otherwise skip.
func TestRenewForceOverridesFreshness(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTestCert(t, dir, "forced", time.Now().Add(80*24*time.Hour))
	out := runRenewScript(t, certPath, keyPath, true)
	if strings.Contains(out, "CERT_FRESH") {
		t.Fatalf("force should bypass the freshness gate, got:\n%s", out)
	}
}

// TestRenewScriptPinsCAOnIssue guards the second half of the outage. acme.sh
// stores a per-domain Le_API; --set-default-ca only moves the default for new
// domains, so a record created against another CA keeps renewing against it and
// fails forever when that CA's account credentials were never stored.
func TestRenewScriptPinsCAOnIssue(t *testing.T) {
	script := renewCertScript("example.test", "/tmp/a.crt", "/tmp/a.key", false, 30)
	var issueLine string
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, "--issue") {
			issueLine = line
			break
		}
	}
	if issueLine == "" {
		t.Fatal("no --issue line in renewal script")
	}
	if !strings.Contains(issueLine, "--server letsencrypt") {
		t.Fatalf("--issue must pin the CA explicitly, got: %s", issueLine)
	}
}

// TestRenewRepairsMachineryBeforeFreshnessGate guards the case that motivated
// the ordering: a node holding a still-valid cert but with no renewal cron is
// an outage with a date on it. If the cron/log setup sat after the gate, those
// nodes would return "fresh" and stay unfixed — the exact shape of the bug this
// change exists to remove, one step later in time.
func TestRenewRepairsMachineryBeforeFreshnessGate(t *testing.T) {
	script := renewCertScript("example.test", "/tmp/a.crt", "/tmp/a.key", false, 30)
	harden := strings.Index(script, "harden_acme\n")
	cron := strings.Index(script, "--install-cronjob")
	gate := strings.Index(script, "CERT_FRESH")
	if harden < 0 || cron < 0 || gate < 0 {
		t.Fatalf("script missing expected parts (harden=%d cron=%d gate=%d)", harden, cron, gate)
	}
	if cron > gate {
		t.Error("--install-cronjob must run before the freshness gate, or a valid-but-uncronned cert stays uncronned")
	}
	if harden > gate {
		t.Error("harden_acme must be invoked before the freshness gate")
	}
}

// TestCertNeedsIssuingMatchesShellGate keeps the Go-side predicate and the
// shell freshness gate from drifting apart. If Go thinks an issue is coming and
// the script disagrees (or vice versa), the DNS precondition gets applied to the
// wrong runs — a CDN node would be failed for DNS it is never going to use.
func TestCertNeedsIssuingMatchesShellGate(t *testing.T) {
	cases := []struct {
		name   string
		before *CertStatus
		force  bool
		want   bool
	}{
		{"expired", &CertStatus{Expired: true, DaysLeft: -8}, false, true},
		{"inside threshold", &CertStatus{DaysLeft: certRenewBeforeDays - 1}, false, true},
		{"on threshold", &CertStatus{DaysLeft: certRenewBeforeDays}, false, false},
		{"comfortable", &CertStatus{DaysLeft: 89}, false, false},
		{"forced", &CertStatus{DaysLeft: 89}, true, true},
		{"unreadable", &CertStatus{Error: "certificate missing or unreadable"}, false, true},
		{"unknown", nil, false, true},
		// The CDN case: a 15-year origin cert must not drag in a DNS check.
		{"origin cert", &CertStatus{DaysLeft: 5407}, false, false},
	}
	for _, tc := range cases {
		if got := certNeedsIssuing(tc.before, tc.force); got != tc.want {
			t.Errorf("%s: certNeedsIssuing = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestParseCertNotAfter(t *testing.T) {
	cases := []struct {
		in   string
		want time.Time
	}{
		{"notAfter=Aug 20 00:32:34 2026 GMT", time.Date(2026, 8, 20, 0, 32, 34, 0, time.UTC)},
		{"Sep 29 10:30:44 2026 GMT", time.Date(2026, 9, 29, 10, 30, 44, 0, time.UTC)},
		// openssl space-pads single-digit days.
		{"notAfter=Aug  1 00:00:00 2027 GMT", time.Date(2027, 8, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		got, err := parseCertNotAfter(tc.in)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.in, err)
		}
		if !got.Equal(tc.want) {
			t.Errorf("parse %q = %v, want %v", tc.in, got, tc.want)
		}
	}
	if _, err := parseCertNotAfter("not a date"); err == nil {
		t.Error("expected an error for unparseable input")
	}
	if _, err := parseCertNotAfter(""); err == nil {
		t.Error("expected an error for empty input")
	}
}

func TestCertTargetsForSkipsReality(t *testing.T) {
	inbounds := []model.NodeInbound{
		{ID: 1, Protocol: "hysteria2", Settings: json.RawMessage(`{"domain":"a.test","cert_path":"/c.crt","key_path":"/c.key"}`)},
		{ID: 2, Protocol: "vless-reality", Settings: json.RawMessage(`{"sni":"www.apple.com"}`)},
		{ID: 3, Protocol: "vless-httpupgrade", Settings: json.RawMessage(`{"domain":"b.test","cert_path":"/b.crt","key_path":"/b.key"}`)},
	}
	targets := certTargetsFor(inbounds)
	if len(targets) != 2 {
		t.Fatalf("expected 2 TLS-terminating targets, got %d", len(targets))
	}
	for _, target := range targets {
		if target.Protocol == "vless-reality" {
			t.Error("reality has no certificate of its own and must not be a renewal target")
		}
	}
	if targets[0].Domain != "a.test" || targets[0].CertPath != "/c.crt" {
		t.Errorf("unexpected target: %+v", targets[0])
	}
}

func TestCertDaysLeftHeadlineTakesWorst(t *testing.T) {
	statuses := map[int]*CertStatus{
		1: {DaysLeft: 70},
		2: {DaysLeft: 12},
		3: {DaysLeft: 45},
	}
	days, ok := certDaysLeftHeadline(statuses)
	if !ok || days != 12 {
		t.Fatalf("headline = %d (%v), want 12", days, ok)
	}

	// An unreadable cert must not read as healthy just because DaysLeft is zero-valued.
	statuses[4] = &CertStatus{Error: "certificate missing or unreadable", Expired: true}
	days, ok = certDaysLeftHeadline(statuses)
	if !ok || days > 0 {
		t.Fatalf("a broken cert must drag the headline to <= 0, got %d", days)
	}

	if _, ok := certDaysLeftHeadline(map[int]*CertStatus{}); ok {
		t.Error("a node with no TLS inbound should report no headline")
	}
}

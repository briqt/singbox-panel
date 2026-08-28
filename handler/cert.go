package handler

// Certificate lifecycle for the inbounds that terminate TLS (hysteria2 and
// vless-httpupgrade).
//
// This is its own path rather than a corner of auto-setup because auto-setup
// decided whether to act by asking "do the cert files exist". An expired file
// exists exactly as happily as a valid one, so a node whose renewal had broken
// stayed broken while auto-setup kept reporting success. Everything here keys
// off the expiry *read back from the node*: writing a cert is not evidence it
// is usable, same rule nodetune.go follows for sysctls.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/briqt/singbox-panel/model"
	"golang.org/x/crypto/ssh"
)

// certRenewBeforeDays is how much life a cert must have left to be left alone.
// Let's Encrypt issues for 90 days and acme.sh's own cron renews at 60, so a
// 30-day floor means an explicit renew only acts on certs that cron already
// failed to handle.
const certRenewBeforeDays = 30

// tlsInboundProtocols are the protocols whose settings carry a cert_path/key_path
// pair. vless-reality borrows a real site's certificate and has none of its own.
var tlsInboundProtocols = map[string]bool{
	"hysteria2":         true,
	"vless-httpupgrade": true,
}

// CertStatus is what a node actually holds on disk for one domain, as read back
// from the node. DaysLeft is truncated toward zero, so a cert with hours left
// reports 0 while still being unexpired.
type CertStatus struct {
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	NotAfter string `json:"not_after,omitempty"`
	DaysLeft int    `json:"days_left"`
	Expired  bool   `json:"expired"`
	Error    string `json:"error,omitempty"`
}

type certTarget struct {
	InboundID int
	Protocol  string
	Domain    string
	CertPath  string
	KeyPath   string
}

// certTargetsFor pulls the TLS material out of inbound settings. Inbounds that
// terminate TLS but were stored without a cert path are returned with empty
// paths so the caller can surface them rather than silently skipping.
func certTargetsFor(inbounds []model.NodeInbound) []certTarget {
	var targets []certTarget
	for _, inbound := range inbounds {
		if !tlsInboundProtocols[inbound.Protocol] {
			continue
		}
		var settings map[string]any
		json.Unmarshal(inbound.Settings, &settings)
		domain, _ := settings["domain"].(string)
		certPath, _ := settings["cert_path"].(string)
		keyPath, _ := settings["key_path"].(string)
		targets = append(targets, certTarget{
			InboundID: inbound.ID, Protocol: inbound.Protocol,
			Domain: domain, CertPath: certPath, KeyPath: keyPath,
		})
	}
	return targets
}

// readCertStatuses reads every cert in one round trip. Node status is polled
// per node page load, so this must not cost one SSH exec per inbound.
func readCertStatuses(client *ssh.Client, targets []certTarget, now time.Time) map[int]*CertStatus {
	result := make(map[int]*CertStatus, len(targets))
	var paths []string
	for _, target := range targets {
		status := &CertStatus{Domain: target.Domain, Path: target.CertPath}
		result[target.InboundID] = status
		if target.CertPath == "" {
			status.Error = "no cert_path in inbound settings"
			continue
		}
		paths = append(paths, target.CertPath)
	}
	if len(paths) == 0 {
		return result
	}

	var b strings.Builder
	for _, path := range paths {
		// One line per cert: "<path>\t<notAfter or empty>". Missing or corrupt
		// files still emit their line so they are reported, not dropped.
		fmt.Fprintf(&b, "printf '%%s\\t' %q; openssl x509 -in %q -noout -enddate 2>/dev/null | sed 's/^notAfter=//'; echo;\n", path, path)
	}
	out, err := sshRun(client, b.String())
	if err != nil && strings.TrimSpace(out) == "" {
		for _, status := range result {
			if status.Error == "" {
				status.Error = "cert read failed: " + err.Error()
			}
		}
		return result
	}

	byPath := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		path, value, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if ok {
			byPath[path] = strings.TrimSpace(value)
		}
	}
	for _, target := range targets {
		status := result[target.InboundID]
		if status.Error != "" || target.CertPath == "" {
			continue
		}
		raw, seen := byPath[target.CertPath]
		if !seen || raw == "" {
			status.Error = "certificate missing or unreadable"
			status.Expired = true
			continue
		}
		notAfter, parseErr := parseCertNotAfter(raw)
		if parseErr != nil {
			status.Error = parseErr.Error()
			continue
		}
		status.NotAfter = notAfter.UTC().Format(time.RFC3339)
		status.DaysLeft = int(notAfter.Sub(now).Hours() / 24)
		status.Expired = !notAfter.After(now)
	}
	return result
}

// parseCertNotAfter reads openssl's -enddate format. openssl space-pads
// single-digit days ("Aug  1 …"), which the _2 layout accepts.
func parseCertNotAfter(raw string) (time.Time, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "notAfter="))
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty enddate")
	}
	for _, layout := range []string{"Jan _2 15:04:05 2006 MST", "Jan _2 15:04:05 2006"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized enddate %q", raw)
}

// renewCertScript issues a certificate and installs it where sing-box reads it.
//
// Two things here are load-bearing and were the cause of a silent outage:
//
//   - The freshness gate runs openssl -checkend, not a file test. "The file is
//     there" and "the cert works" are different claims.
//   - --server is repeated on --issue. --set-default-ca only moves the default
//     for *new* domains; acme.sh keeps a per-domain Le_API, so a record first
//     created against another CA keeps renewing against that CA forever. If that
//     CA needs account credentials nobody stored (ZeroSSL wants EAB), every
//     renewal fails and the cert expires with no signal.
func renewCertScript(domain, certPath, keyPath string, force bool, minDays int) string {
	forceFlag := "0"
	if force {
		forceFlag = "1"
	}
	return fmt.Sprintf(`set -u
DOMAIN=%q; CERT=%q; KEY=%q; FORCE=%q; MINDAYS=%d
ACME=/root/.acme.sh/acme.sh
mkdir -p "$(dirname "$CERT")"

# Repair the renewal *machinery* on every run, before deciding whether this
# particular cert needs re-issuing. A node can hold a perfectly valid cert and
# still have no cron to renew it — that is not a healthy node, it is an outage
# with a date on it. Gating this behind "we happened to re-issue today" would
# leave exactly those nodes unfixed.
harden_acme() {
  [ -x "$ACME" ] || return 0
  # acme.sh writes no log unless LOG_FILE is set, and the cron entry it installs
  # sends stdout to /dev/null, so a failing renewal is silent until expiry.
  touch /root/.acme.sh/account.conf
  grep -q '^LOG_FILE=' /root/.acme.sh/account.conf || echo "LOG_FILE='/root/.acme.sh/acme.sh.log'" >> /root/.acme.sh/account.conf
  grep -q '^LOG_LEVEL=' /root/.acme.sh/account.conf || echo "LOG_LEVEL=1" >> /root/.acme.sh/account.conf
  "$ACME" --install-cronjob >/dev/null 2>&1
  echo "ACME_HARDENED"
}
harden_acme

if [ "$FORCE" != "1" ] && [ -f "$CERT" ] && [ -f "$KEY" ] &&
   openssl x509 -in "$CERT" -noout -checkend $((MINDAYS * 86400)) >/dev/null 2>&1; then
  echo "CERT_FRESH"; exit 0
fi

if [ ! -x "$ACME" ]; then
  curl -sL https://get.acme.sh | sh -s email=acme@"$DOMAIN" 2>&1
  harden_acme
fi
if [ ! -x "$ACME" ]; then echo "ACME_MISSING"; exit 0; fi

"$ACME" --set-default-ca --server letsencrypt 2>/dev/null

ACME_MODE="--standalone"
if command -v caddy >/dev/null 2>&1 && systemctl is-active caddy >/dev/null 2>&1; then
  WEBROOT="/var/www/acme"
  mkdir -p "$WEBROOT"
  ACME_MODE="--webroot $WEBROOT"
  if ! grep -q "$DOMAIN" /etc/caddy/Caddyfile 2>/dev/null; then
    printf '\nhttp://%%s {\n  root * /var/www/acme\n  file_server\n}\n' "$DOMAIN" >> /etc/caddy/Caddyfile
    systemctl reload caddy 2>/dev/null; sleep 1
  fi
elif ss -tlnp 2>/dev/null | grep -q ':80 '; then
  PORT80_SVC=$(ss -tlnp 2>/dev/null | grep ':80 ' | grep -oP 'users:\(\("\K[^"]+' || true)
  if [ -n "${PORT80_SVC:-}" ]; then systemctl stop "$PORT80_SVC" 2>/dev/null || true; sleep 1; fi
fi

"$ACME" --issue -d "$DOMAIN" $ACME_MODE --keylength ec-256 --server letsencrypt --force 2>&1
ISSUE_RC=$?
if [ -n "${PORT80_SVC:-}" ]; then systemctl start "$PORT80_SVC" 2>/dev/null || true; fi

if [ "$ISSUE_RC" != "0" ]; then
  echo "ACME_ISSUE_FAILED rc=$ISSUE_RC"
  exit 0
fi

"$ACME" --install-cert -d "$DOMAIN" --ecc --fullchain-file "$CERT" --key-file "$KEY" \
  --reloadcmd "systemctl restart sing-box 2>/dev/null || true" 2>&1
echo "ACME_DONE"
`, domain, certPath, keyPath, forceFlag, minDays)
}

type CertRenewReq struct {
	Domain string `json:"domain"`
	Force  bool   `json:"force"`
}

type certRenewResult struct {
	Domain     string      `json:"domain"`
	Protocol   string      `json:"protocol"`
	Status     string      `json:"status"` // renewed | fresh | failed
	Before     *CertStatus `json:"before,omitempty"`
	After      *CertStatus `json:"after,omitempty"`
	Details    string      `json:"details,omitempty"`
	AcmeOutput string      `json:"acme_output,omitempty"`
}

// HandleCertRenew re-issues the certificates a node's TLS inbounds depend on,
// without touching ports, UUIDs or Reality keys — re-running auto-setup to fix
// a cert would risk rewriting inbound identity that live subscriptions encode.
func (h *NodeOpsHandler) HandleCertRenew(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	nodeID := parseNodeIDFromConfigPath(r.URL.Path)
	node, err := h.Nodes.Get(nodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}

	var req CertRenewReq
	json.NewDecoder(r.Body).Decode(&req)
	if req.Domain != "" && !validDomainName(req.Domain) {
		writeError(w, http.StatusBadRequest, "invalid domain")
		return
	}

	inbounds, err := h.Nodes.ListInbounds(node.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	targets := certTargetsFor(inbounds)
	if req.Domain != "" {
		filtered := targets[:0:0]
		for _, target := range targets {
			if target.Domain == req.Domain {
				filtered = append(filtered, target)
			}
		}
		targets = filtered
	}
	if len(targets) == 0 {
		writeError(w, http.StatusBadRequest, "node has no TLS-terminating inbound to renew")
		return
	}

	client, err := h.Config.sshConnect(node)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ssh connect failed: "+err.Error())
		return
	}
	defer client.Close()

	now := time.Now()
	before := readCertStatuses(client, targets, now)
	results := make([]certRenewResult, 0, len(targets))
	renewed := 0

	for _, target := range targets {
		result := certRenewResult{Domain: target.Domain, Protocol: target.Protocol, Before: before[target.InboundID]}
		if target.Domain == "" || target.CertPath == "" || target.KeyPath == "" {
			result.Status, result.Details = "failed", "inbound settings lack domain/cert_path/key_path"
			results = append(results, result)
			continue
		}
		// Only gate on DNS when this run will actually try to issue. A CDN node
		// points its domain at the CDN on purpose and carries a manually
		// uploaded origin cert; failing it for "DNS does not point here" would
		// report a problem that does not exist, and would also block the
		// cron/log repair the script performs before it looks at the cert.
		if certNeedsIssuing(result.Before, req.Force) {
			// HTTP-01 answers on the node itself, so a domain pointed elsewhere
			// cannot be validated here. Say so rather than letting acme.sh fail
			// with a wall of debug output.
			if ips, lookupErr := net.LookupHost(target.Domain); lookupErr != nil {
				result.Status = "failed"
				result.Details = "DNS lookup failed: " + lookupErr.Error()
				results = append(results, result)
				continue
			} else if !containsHost(ips, node.Host) {
				result.Status = "failed"
				result.Details = fmt.Sprintf("DNS: %s → %v, expected %s; HTTP-01 validates against the node itself", target.Domain, ips, node.Host)
				results = append(results, result)
				continue
			}
		}

		out, runErr := sshRun(client, renewCertScript(target.Domain, target.CertPath, target.KeyPath, req.Force, certRenewBeforeDays))
		result.AcmeOutput = tailLines(out, 20)
		if contains(out, "CERT_FRESH") {
			result.Status = "fresh"
			result.After = result.Before
			result.Details = fmt.Sprintf("still valid for %d days; pass force to re-issue anyway", result.Before.DaysLeft)
			results = append(results, result)
			continue
		}

		// Read back rather than trusting acme.sh's exit path: the only claim
		// that matters is what sing-box will load from disk.
		after := readCertStatuses(client, []certTarget{target}, time.Now())[target.InboundID]
		result.After = after
		switch {
		case after.Error != "" || after.Expired:
			result.Status = "failed"
			result.Details = firstNonEmpty(after.Error, "certificate still expired after renewal")
			if runErr != nil {
				result.Details += "; ssh: " + runErr.Error()
			}
		case result.Before != nil && result.Before.NotAfter == after.NotAfter:
			result.Status = "failed"
			result.Details = "acme.sh ran but the installed certificate did not change"
		default:
			result.Status = "renewed"
			renewed++
		}
		results = append(results, result)
	}

	status := http.StatusOK
	for _, result := range results {
		if result.Status == "failed" {
			status = http.StatusMultiStatus
		}
	}
	writeJSON(w, status, map[string]any{"node": node.Name, "renewed": renewed, "certs": results})
}

// certNeedsIssuing mirrors the freshness gate inside renewCertScript. It exists
// so the Go side can tell "we are about to ask a CA for a certificate" from
// "we are only here to repair cron and logging", which have different
// preconditions: only the former needs the domain to resolve to this node.
func certNeedsIssuing(before *CertStatus, force bool) bool {
	if force || before == nil {
		return true
	}
	return before.Error != "" || before.Expired || before.DaysLeft < certRenewBeforeDays
}

func containsHost(ips []string, host string) bool {
	for _, ip := range ips {
		if ip == host {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// tailLines keeps the end of acme.sh's output, where its failures are stated.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// certDaysLeftHeadline reduces a node's certs to the single worst number, which
// is what the node list needs to colour a row.
func certDaysLeftHeadline(statuses map[int]*CertStatus) (int, bool) {
	worst, found := 0, false
	for _, status := range statuses {
		days := status.DaysLeft
		if status.Error != "" || status.Expired {
			days = min(days, 0)
		}
		if !found || days < worst {
			worst, found = days, true
		}
	}
	return worst, found
}

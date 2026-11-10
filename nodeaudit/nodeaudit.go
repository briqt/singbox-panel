// Package nodeaudit ships the node-side whole-machine traffic collector.
//
// The collector used to be a script copied onto one node by hand. Anything that
// runs on every node has to be a panel code path instead — reviewable, tested,
// released, and re-runnable — so the script and its systemd units are embedded
// here and installed through POST /api/nodes/{id}/traffic-audit.
package nodeaudit

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/briqt/singbox-panel/model"
)

//go:embed traffic-audit.py
var Script []byte

//go:embed traffic-audit.service
var ServiceUnit []byte

//go:embed traffic-audit.timer
var TimerUnit []byte

const (
	ScriptPath  = "/usr/local/libexec/traffic-audit.py"
	ServicePath = "/etc/systemd/system/traffic-audit.service"
	TimerPath   = "/etc/systemd/system/traffic-audit.timer"
	ConfigPath  = "/etc/traffic-audit.json"
	DataDir     = "/var/lib/traffic-audit"
	// MinFreeBytes refuses an install on a nearly full disk. The collector caps
	// itself at 48 MiB of database and pauses below 192 MiB free; this keeps a
	// wide margin so the budget is never what tips a node over.
	MinFreeBytes = 1 << 30
)

// Python is how every remote invocation runs the script: isolated mode, so a
// stray module in the working directory can never shadow the stdlib.
const Python = "/usr/bin/python3 -I " + ScriptPath

// ScriptSHA256 identifies the embedded build, so status can tell whether a node
// runs the version this panel would install.
func ScriptSHA256() string {
	sum := sha256.Sum256(Script)
	return hex.EncodeToString(sum[:])
}

// Port is one server port the collector counts separately.
type Port struct {
	Proto string `json:"proto"`
	Port  int    `json:"port"`
	Label string `json:"label"`
}

// Desired is what the panel tells the node to count. The node merges in its own
// listening sockets on `configure`, so services the panel does not manage
// (Caddy, DERP, …) are still counted.
type Desired struct {
	Interface string   `json:"interface,omitempty"`
	Ports     []Port   `json:"ports"`
	Units     []string `json:"units"`
}

var labelUnsafe = regexp.MustCompile(`[^a-z0-9_]+`)

func label(s string) string {
	l := strings.Trim(labelUnsafe.ReplaceAllString(strings.ToLower(s), "_"), "_")
	if len(l) > 24 {
		l = strings.Trim(l[:24], "_")
	}
	if l == "" {
		return "port"
	}
	return l
}

// DesiredFor derives the counted ports from the node's inbounds and its SSH
// port. Labels come from inbound tags, which are what the operator sees in the
// panel; the node falls back to process names for everything else.
func DesiredFor(node *model.Node, inbounds []model.NodeInbound, iface string) Desired {
	d := Desired{Interface: iface, Units: []string{"sing-box.service"}}
	seen := map[string]bool{}
	add := func(proto string, port int, name string) {
		key := fmt.Sprintf("%s/%d", proto, port)
		if port <= 0 || port > 65535 || seen[key] {
			return
		}
		seen[key] = true
		d.Ports = append(d.Ports, Port{Proto: proto, Port: port, Label: label(name)})
	}
	sorted := append([]model.NodeInbound(nil), inbounds...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Port < sorted[j].Port })
	for _, ib := range sorted {
		if !ib.Enabled {
			continue
		}
		proto := "tcp"
		if ib.Protocol == "hysteria2" {
			proto = "udp"
		}
		name := ib.Tag
		if name == "" {
			name = ib.Protocol
		}
		add(proto, ib.Port, name)
	}
	if node != nil {
		port := node.Port
		if port == 0 {
			port = 22
		}
		add("tcp", port, "ssh")
	}
	return d
}

func (d Desired) JSON() []byte {
	b, _ := json.Marshal(d)
	return b
}

// PreflightScript checks what the collector needs and how much room it has. It
// prints one JSON object; the caller decides whether to proceed.
func PreflightScript() string {
	return `set -u
py=$(/usr/bin/python3 -c 'import sys;print("%d.%d"%sys.version_info[:2])' 2>/dev/null || echo none)
nft_ok=no; command -v nft >/dev/null 2>&1 && nft_ok=yes
ss_ok=no; command -v ss >/dev/null 2>&1 && ss_ok=yes
ip_ok=no; command -v ip >/dev/null 2>&1 && ip_ok=yes
cg=$(stat -fc %T /sys/fs/cgroup 2>/dev/null || echo unknown)
mkdir -p /var/lib
free=$(df -B1 --output=avail /var/lib 2>/dev/null | tail -1 | tr -d ' ')
nft_ver=$(nft --version 2>/dev/null | awk '{print $2}')
printf '{"python":"%s","nft":"%s","nft_version":"%s","ss":"%s","ip":"%s","cgroup":"%s","free_bytes":%s}\n' \
  "$py" "$nft_ok" "${nft_ver:-}" "$ss_ok" "$ip_ok" "$cg" "${free:-0}"`
}

// InstallNFTScript installs the nft userspace tool on apt-based systems. Only the
// CLI is needed: the collector loads its own table at runtime. The distro's
// nftables.service stays disabled — its stock /etc/nftables.conf begins with
// "flush ruleset", and starting it would wipe every other firewall rule — so
// the script fails if installing the package left that service enabled.
func InstallNFTScript() string {
	return `set -eu
command -v nft >/dev/null 2>&1 && { echo already; exit 0; }
command -v apt-get >/dev/null 2>&1 || { echo "nft missing and no apt-get; install nftables manually" >&2; exit 2; }
before=$(systemctl is-enabled nftables 2>/dev/null || echo absent)
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends nftables >/dev/null 2>&1 || {
  DEBIAN_FRONTEND=noninteractive apt-get update >/dev/null 2>&1
  DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends nftables >/dev/null
}
command -v nft >/dev/null 2>&1
after=$(systemctl is-enabled nftables 2>/dev/null || echo absent)
if [ "$after" = enabled ] && [ "$before" != enabled ]; then
  systemctl disable nftables >/dev/null 2>&1
  echo "installed; nftables.service was auto-enabled and has been disabled again (not started)"
else
  echo installed
fi
systemctl is-active --quiet nftables && { echo "nftables.service is active" >&2; exit 3; } || true`
}

// Preflight is the parsed PreflightScript output.
type Preflight struct {
	Python     string `json:"python"`
	NFT        string `json:"nft"`
	NFTVersion string `json:"nft_version"`
	SS         string `json:"ss"`
	IP         string `json:"ip"`
	Cgroup     string `json:"cgroup"`
	FreeBytes  int64  `json:"free_bytes"`
}

// Problems lists what blocks an install. An empty list means go ahead.
// Service accounting needs cgroup v2; without it the NIC/port/peer counters
// still work, so that is a warning, not a blocker.
func (p Preflight) Problems() (blockers, warnings []string) {
	if !pythonAtLeast(p.Python, 3, 9) {
		blockers = append(blockers, "python3 >= 3.9 is required, found "+p.Python)
	}
	if p.NFT != "yes" {
		blockers = append(blockers, "nft (nftables) is not installed")
	}
	if p.SS != "yes" || p.IP != "yes" {
		blockers = append(blockers, "iproute2 (ss, ip) is required")
	}
	if p.FreeBytes < MinFreeBytes {
		blockers = append(blockers, fmt.Sprintf("only %d bytes free under /var/lib; need at least 1 GiB", p.FreeBytes))
	}
	if p.Cgroup != "cgroup2fs" {
		warnings = append(warnings, "cgroup v2 not mounted at /sys/fs/cgroup; per-service accounting will be unavailable")
	}
	return blockers, warnings
}

func pythonAtLeast(v string, major, minor int) bool {
	var a, b int
	if _, err := fmt.Sscanf(v, "%d.%d", &a, &b); err != nil {
		return false
	}
	return a > major || (a == major && b >= minor)
}

// ActivateScript runs after the files are staged as *.new: verifies the new
// script parses under the node's python, swaps everything into place, applies
// the configuration, enables the timer, and takes one sample synchronously so
// the caller can read back a real result instead of trusting the exit code.
func ActivateScript() string {
	return `set -eu
/usr/bin/python3 -I ` + ScriptPath + `.new version >/dev/null
chmod 0755 ` + ScriptPath + `.new && mv -f ` + ScriptPath + `.new ` + ScriptPath + `
chmod 0644 ` + ServicePath + `.new ` + TimerPath + `.new
mv -f ` + ServicePath + `.new ` + ServicePath + `
mv -f ` + TimerPath + `.new ` + TimerPath + `
` + Python + ` configure < ` + ConfigPath + `.desired >/dev/null
rm -f ` + ConfigPath + `.desired
systemctl daemon-reload
systemctl enable --now traffic-audit.timer >/dev/null 2>&1
systemctl start traffic-audit.service || true
` + Python + ` status`
}

// UninstallScript restores what the collector changed (runtime IPAccounting,
// its own nft table) and removes its files. Data goes too: it is a buffer, and
// the panel already holds the hourly conclusions.
func UninstallScript() string {
	return `set -u
systemctl disable --now traffic-audit.timer >/dev/null 2>&1 || true
systemctl stop traffic-audit.service >/dev/null 2>&1 || true
out='{"ok":true,"note":"script was not installed"}'
if [ -f ` + ScriptPath + ` ]; then out=$(` + Python + ` uninstall) || exit 1; fi
rm -f ` + ScriptPath + ` ` + ServicePath + ` ` + TimerPath + ` ` + ConfigPath + `
rm -rf ` + DataDir + `
systemctl daemon-reload
echo "$out"`
}

// Status is the parsed `traffic-audit.py status` output.
type Status struct {
	Version       string          `json:"version"`
	Installed     bool            `json:"installed"`
	Timer         string          `json:"timer"`
	Interface     string          `json:"interface"`
	FirstSample   float64         `json:"first_sample,omitempty"`
	LastSample    float64         `json:"last_sample,omitempty"`
	LastSampleAge float64         `json:"last_sample_age,omitempty"`
	StorageBytes  int64           `json:"storage_bytes"`
	LastError     json.RawMessage `json:"last_error,omitempty"`
	ConfigError   string          `json:"config_error,omitempty"`
	Config        *struct {
		Ports     []Port   `json:"ports"`
		Units     []string `json:"units"`
		Interface string   `json:"interface"`
	} `json:"config,omitempty"`
	Limits json.RawMessage `json:"limits,omitempty"`
}

// Healthy is the read-back criterion: the timer is active, the last sample is
// recent, and no collection error is pending. "The unit file exists" is not it.
func (s Status) Healthy() (bool, string) {
	switch {
	case !s.Installed:
		return false, "not installed"
	case s.Timer != "active":
		return false, "timer is " + s.Timer
	case len(s.LastError) > 0 && string(s.LastError) != "null":
		return false, "last collection failed: " + string(s.LastError)
	case s.LastSample == 0:
		return false, "no sample recorded yet"
	case s.LastSampleAge > 180:
		return false, fmt.Sprintf("last sample is %.0fs old", s.LastSampleAge)
	}
	return true, ""
}

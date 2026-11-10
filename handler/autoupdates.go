package handler

import (
	"sort"
	"strings"

	"golang.org/x/crypto/ssh"
)

// No automatic updates on nodes.
//
// These are single-purpose servers whose software the panel pins and upgrades
// deliberately (sing-box via /install, the OS by an operator). Unattended
// package installs can restart services underneath the panel, and the periodic
// update checks are not free on small nodes: on a 454 MB Tokyo node the twice-
// daily apt run's motd hook (apt-check) sat in page-reclaim for half an hour and
// held the apt lock. So the baseline turns all of it off and reads the result
// back. Manual apt keeps working; nothing is uninstalled.

// autoUpdateTimers are the scheduling units across Debian/Ubuntu releases. Only
// the ones present on a node are touched.
var autoUpdateTimers = []string{
	"apt-daily.timer", "apt-daily-upgrade.timer",
	"update-notifier-download.timer", "update-notifier-motd.timer",
	"motd-news.timer", "ua-timer.timer", "snapd.snap-repair.timer",
}

const noAutoUpdatesAptConf = "/etc/apt/apt.conf.d/99-no-automatic-updates"

// wantAptPeriodic is the effective apt configuration a node must report.
var wantAptPeriodic = map[string]string{
	"APT::Periodic::Update-Package-Lists":          "0",
	"APT::Periodic::Unattended-Upgrade":            "0",
	"APT::Periodic::Download-Upgradeable-Packages": "0",
}

func noAutoUpdatesScript() string {
	return `set -u
T="` + strings.Join(autoUpdateTimers, " ") + `"
for t in $T; do
  systemctl cat "$t" >/dev/null 2>&1 || continue
  systemctl disable --now "$t" >/dev/null 2>&1 || true
  systemctl mask "$t" >/dev/null 2>&1 || true
done
if [ -d /etc/apt/apt.conf.d ]; then
  cat > ` + noAutoUpdatesAptConf + ` <<'EOF'
// Managed by singbox-panel: no automatic update checks or upgrades on nodes.
// Run apt manually when you decide to update.
APT::Periodic::Enable "0";
APT::Periodic::Update-Package-Lists "0";
APT::Periodic::Download-Upgradeable-Packages "0";
APT::Periodic::AutocleanInterval "0";
APT::Periodic::Unattended-Upgrade "0";
EOF
  chmod 0644 ` + noAutoUpdatesAptConf + `
fi
[ -e /etc/update-motd.d/50-motd-news ] && printf '# Managed by singbox-panel\nENABLED=0\n' > /etc/default/motd-news
[ -f /etc/update-manager/release-upgrades ] && sed -i 's/^Prompt=.*/Prompt=never/' /etc/update-manager/release-upgrades
if command -v snap >/dev/null 2>&1; then snap refresh --hold >/dev/null 2>&1 && echo "snap held" || echo "snap hold-failed"; fi
systemctl daemon-reload
# Read back what is actually in effect.
for t in $T; do
  systemctl cat "$t" >/dev/null 2>&1 || continue
  echo "timer $t $(systemctl is-enabled "$t" 2>/dev/null || true) $(systemctl is-active "$t" 2>/dev/null || true)"
done
command -v apt-config >/dev/null 2>&1 && apt-config dump | grep -E '^APT::Periodic::' | sed 's/^/apt /'
true`
}

// AutoUpdatesResult reports the read-back. Ineffective is what matters: a
// timer still enabled or an apt key overridden by a later apt.conf.d file.
type AutoUpdatesResult struct {
	Disabled    []string          `json:"disabled"`
	Ineffective map[string]string `json:"ineffective,omitempty"`
	Snap        string            `json:"snap,omitempty"`
}

func parseAutoUpdatesReadback(out string) *AutoUpdatesResult {
	res := &AutoUpdatesResult{Ineffective: map[string]string{}}
	apt := map[string]string{}
	sawApt := false
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) >= 3 && f[0] == "timer":
			enabled := f[2]
			active := ""
			if len(f) >= 4 {
				active = f[3]
			}
			if enabled == "masked" && active != "active" {
				res.Disabled = append(res.Disabled, f[1])
			} else {
				res.Ineffective[f[1]] = enabled + "/" + active
			}
		case len(f) >= 3 && f[0] == "apt":
			sawApt = true
			apt[f[1]] = strings.Trim(strings.TrimSuffix(f[2], ";"), `"`)
		case len(f) >= 2 && f[0] == "snap":
			res.Snap = f[1]
		}
	}
	if sawApt {
		keys := make([]string, 0, len(wantAptPeriodic))
		for k := range wantAptPeriodic {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if got, ok := apt[k]; !ok {
				res.Ineffective[k] = "missing"
			} else if got != wantAptPeriodic[k] {
				res.Ineffective[k] = got
			}
		}
	}
	sort.Strings(res.Disabled)
	if len(res.Ineffective) == 0 {
		res.Ineffective = nil
	}
	return res
}

func disableAutoUpdates(client *ssh.Client) (*AutoUpdatesResult, error) {
	out, err := sshRun(client, noAutoUpdatesScript())
	if err != nil {
		return nil, err
	}
	return parseAutoUpdatesReadback(out), nil
}

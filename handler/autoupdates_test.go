package handler

import (
	"os/exec"
	"strings"
	"testing"
)

const hardenedReadback = `timer apt-daily.timer masked inactive
timer apt-daily-upgrade.timer masked inactive
timer motd-news.timer masked inactive
apt APT::Periodic::Enable "0";
apt APT::Periodic::Update-Package-Lists "0";
apt APT::Periodic::Download-Upgradeable-Packages "0";
apt APT::Periodic::AutocleanInterval "0";
apt APT::Periodic::Unattended-Upgrade "0";
`

func TestAutoUpdatesReadbackAcceptsDisabledNode(t *testing.T) {
	res := parseAutoUpdatesReadback(hardenedReadback)
	if len(res.Ineffective) != 0 || len(res.Disabled) != 3 {
		t.Fatalf("got %+v", res)
	}
}

// Falsification: each way the baseline can silently not take effect must be
// reported, by value — "0" vs "1", not a substring match.
func TestAutoUpdatesReadbackCatchesWhatItGuards(t *testing.T) {
	for name, out := range map[string]string{
		"timer still enabled":      strings.Replace(hardenedReadback, "apt-daily.timer masked inactive", "apt-daily.timer enabled active", 1),
		"masked but still running": strings.Replace(hardenedReadback, "motd-news.timer masked inactive", "motd-news.timer masked active", 1),
		"later apt.conf overrides": strings.Replace(hardenedReadback, `Unattended-Upgrade "0"`, `Unattended-Upgrade "1"`, 1),
		"lists still refreshed":    strings.Replace(hardenedReadback, `Update-Package-Lists "0"`, `Update-Package-Lists "10"`, 1),
		"apt key missing entirely": strings.Replace(hardenedReadback, "apt APT::Periodic::Download-Upgradeable-Packages \"0\";\n", "", 1),
	} {
		if res := parseAutoUpdatesReadback(out); len(res.Ineffective) != 1 {
			t.Fatalf("%s: expected one ineffective item, got %+v", name, res)
		}
	}
}

func TestNoAutoUpdatesScriptParsesAndNeverUninstalls(t *testing.T) {
	script := noAutoUpdatesScript()
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script does not parse: %v\n%s", err, out)
	}
	for _, forbidden := range []string{"apt-get remove", "apt-get purge", "systemctl restart", "reboot"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("baseline must only stop scheduling, found %q", forbidden)
		}
	}
}

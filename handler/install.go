package handler

import (
	"fmt"
	"strings"
)

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// Validate the candidate before replacing a working binary. A running service
// must actually run the new version, or restore its previous binary.
func verifiedInstallScript(url, binPath, configPath, version string) string {
	return fmt.Sprintf(`set -eu
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
curl -fSL --retry 2 --connect-timeout 15 --max-time 180 %s -o "$work/sb.tar.gz"
tar xzf "$work/sb.tar.gz" -C "$work"
candidate=$(find "$work" -name sing-box -type f | head -1)
[ -n "$candidate" ]
version_out=$("$candidate" version)
printf '%%s\n' "$version_out"
printf '%%s\n' "$version_out" | grep -Fx %s
printf '%%s\n' "$version_out" | grep -q 'with_v2ray_api'
if [ -s %s ]; then "$candidate" check -c %s; fi
mkdir -p "$(dirname %s)"
was_running=0
systemctl is-active --quiet sing-box && was_running=1
had_binary=0
if [ -f %s ]; then cp -p %s %s; had_binary=1; fi
install -m 0755 "$candidate" %s
mv -f %s %s
if [ "$was_running" = 1 ]; then
  running_new_binary() {
    systemctl is-active --quiet sing-box || return 1
    pid=$(systemctl show -p MainPID --value sing-box)
    [ -n "$pid" ] && [ "$pid" != 0 ] || return 1
    [ "$(readlink -f /proc/"$pid"/exe)" = "$(readlink -f %s)" ]
  }
  if ! systemctl restart sing-box || ! sleep 2 || ! running_new_binary; then
    if [ "$had_binary" = 1 ]; then mv -f %s %s; systemctl restart sing-box; fi
    echo 'upgrade failed; previous binary restored' >&2
    exit 1
  fi
fi
%s version | grep -Fx %s
`, shellQuote(url), shellQuote("sing-box version "+version), shellQuote(configPath), shellQuote(configPath),
		shellQuote(binPath), shellQuote(binPath), shellQuote(binPath), shellQuote(binPath+".previous"),
		shellQuote(binPath+".new"), shellQuote(binPath+".new"), shellQuote(binPath),
		shellQuote(binPath),
		shellQuote(binPath+".previous"), shellQuote(binPath), shellQuote(binPath), shellQuote("sing-box version "+version))
}

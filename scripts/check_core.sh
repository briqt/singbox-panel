#!/bin/sh
# Run from the repository on a Linux test host. Uses only temporary files and
# loopback listeners; existing sing-box configuration and services are untouched.
set -eu
version=${SINGBOX_VERSION:-1.14.4}
case "$(uname -m)" in
  x86_64) arch=amd64;;
  aarch64|arm64) arch=arm64;;
  *) echo 'unsupported architecture' >&2; exit 1;;
esac
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
curl -fSL --retry 2 --max-time 180 "https://github.com/briqt/sing-box/releases/download/v$version/sing-box-$version-linux-$arch.tar.gz" -o "$work/core.tar.gz"
tar xzf "$work/core.tar.gz" -C "$work"
core=$(find "$work" -name sing-box -type f | head -1)
"$core" version
# A precompiled test binary lets a host without Go run the same test.
if [ -n "${SINGBOX_CORE_TEST:-}" ]; then
  SINGBOX_TEST_BIN="$core" "$SINGBOX_CORE_TEST" -test.run '^TestCoreHysteriaProfiles$' -test.v
else
  SINGBOX_TEST_BIN="$core" go test ./singbox -run '^TestCoreHysteriaProfiles$' -v -count=1
fi

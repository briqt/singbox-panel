package handler

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Node kernel baseline.
//
// A stock Linux ships net.core.rmem_max/wmem_max at 208 KB. Hysteria2 is QUIC
// over UDP, and quic-go asks for a receive buffer far above that, so an
// untuned node silently caps throughput no matter how fast the link is. The
// TCP side (VLESS Reality) wants BBR + fq for the same reason.
//
// This runs as part of node setup so a freshly added node is tuned the same way
// as every other one, instead of depending on whoever remembered to do it by
// hand.
const tuneDropInPath = "/etc/sysctl.d/99-zz-singbox-panel.conf"

// tunedSysctls is the baseline, rendered per node. Buffer ceilings scale with
// RAM: they are ceilings rather than allocations, but a small node should not
// advertise a limit its global udp_mem pool cannot back.
func tunedSysctls(memTotalKB int64) map[string]string {
	// Below ~1 GB use 8 MB, which still clears quic-go's ask; above it use 16 MB.
	bufMax := 16 * 1024 * 1024
	if memTotalKB > 0 && memTotalKB < 1024*1024 {
		bufMax = 8 * 1024 * 1024
	}
	buf := strconv.Itoa(bufMax)
	return map[string]string{
		"net.core.rmem_max":                  buf,
		"net.core.wmem_max":                  buf,
		"net.core.rmem_default":              "1048576",
		"net.core.wmem_default":              "1048576",
		"net.ipv4.udp_rmem_min":              "8192",
		"net.ipv4.udp_wmem_min":              "8192",
		"net.core.netdev_max_backlog":        "16384",
		"net.core.somaxconn":                 "8192",
		"net.ipv4.tcp_mtu_probing":           "1",
		"net.ipv4.tcp_slow_start_after_idle": "0",
		"net.core.default_qdisc":             "fq",
		"net.ipv4.tcp_congestion_control":    "bbr",
	}
}

// KernelTuneResult reports what the node ended up with. Applied lists keys whose
// effective value matches what was asked for; Ineffective lists keys that do
// not, which is the case that matters: /etc/sysctl.conf is read *after*
// everything in /etc/sysctl.d, so a stale tuning script there silently wins.
// Writing the file is not evidence the value took, so the panel reads it back.
type KernelTuneResult struct {
	Applied     []string          `json:"applied"`
	Ineffective map[string]string `json:"ineffective,omitempty"` // key -> effective value
	Note        string            `json:"note,omitempty"`
}

// applyKernelTuning writes the drop-in, loads it, then verifies each key by
// reading the running value back.
func applyKernelTuning(client *ssh.Client) (*KernelTuneResult, error) {
	memKB := readMemTotalKB(client)
	want := tunedSysctls(memKB)

	var b strings.Builder
	b.WriteString("# Managed by singbox-panel. Baseline for hysteria2 (QUIC/UDP) and\n")
	b.WriteString("# VLESS Reality (TCP). Regenerated on every node setup.\n")
	b.WriteString("# Sorted last in /etc/sysctl.d on purpose; note that /etc/sysctl.conf\n")
	b.WriteString("# is still applied after this file and will override it.\n")
	for _, k := range sortedKeys(want) {
		fmt.Fprintf(&b, "%s = %s\n", k, want[k])
	}

	if err := sshWriteFile(client, tuneDropInPath, []byte(b.String())); err != nil {
		return nil, fmt.Errorf("write %s: %w", tuneDropInPath, err)
	}
	// BBR may be a module on stock kernels; load it and make that persist.
	sshRun(client, "modprobe tcp_bbr 2>/dev/null; grep -qxs tcp_bbr /etc/modules-load.d/bbr.conf 2>/dev/null || echo tcp_bbr > /etc/modules-load.d/bbr.conf")
	if out, err := sshRun(client, "sysctl --system >/dev/null 2>&1; echo done"); err != nil {
		return nil, fmt.Errorf("sysctl --system: %s: %w", out, err)
	}

	result := &KernelTuneResult{Ineffective: map[string]string{}}
	for _, k := range sortedKeys(want) {
		out, err := sshRun(client, "sysctl -n "+k+" 2>/dev/null")
		got := normalizeSysctl(out)
		switch {
		case err != nil || got == "":
			result.Ineffective[k] = "unreadable"
		case got == want[k]:
			result.Applied = append(result.Applied, k)
		default:
			result.Ineffective[k] = got
		}
	}
	if len(result.Ineffective) > 0 {
		result.Note = "some values did not take effect; /etc/sysctl.conf and /etc/sysctl.d/99-sysctl.conf are applied after " +
			tuneDropInPath + " and override it. Remove the conflicting keys there."
	}
	return result, nil
}

func readMemTotalKB(client *ssh.Client) int64 {
	out, err := sshRun(client, "awk '/^MemTotal:/{print $2}' /proc/meminfo")
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(normalizeSysctl(out), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// normalizeSysctl collapses the tabs sysctl uses between vector values so a
// read-back can be compared against the scalar we wrote.
func normalizeSysctl(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Small fixed set; insertion sort keeps this dependency-free and stable.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

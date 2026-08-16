package handler

import (
	"strconv"
	"testing"
)

func TestTunedSysctlsScaleWithMemory(t *testing.T) {
	small := tunedSysctls(454 * 1024)  // ~454 MB, like a 512 MB VPS
	large := tunedSysctls(2468 * 1024) // ~2.4 GB
	unknown := tunedSysctls(0)         // /proc/meminfo unreadable

	if small["net.core.rmem_max"] != strconv.Itoa(8*1024*1024) {
		t.Fatalf("small node rmem_max = %s, want 8Mi", small["net.core.rmem_max"])
	}
	if large["net.core.rmem_max"] != strconv.Itoa(16*1024*1024) {
		t.Fatalf("large node rmem_max = %s, want 16Mi", large["net.core.rmem_max"])
	}
	// An unreadable meminfo must not shrink the node to the small tier by
	// accident; the default is the larger ceiling.
	if unknown["net.core.rmem_max"] != large["net.core.rmem_max"] {
		t.Fatalf("unknown memory = %s, want the same as a large node", unknown["net.core.rmem_max"])
	}
}

// The whole point of the baseline is that a stock node's 208 KB ceiling is far
// below what quic-go asks for. Pin that we clear it by a wide margin.
func TestTunedBuffersClearStockDefault(t *testing.T) {
	const stockDefault = 212992
	for _, memKB := range []int64{454 * 1024, 2468 * 1024, 0} {
		got, err := strconv.Atoi(tunedSysctls(memKB)["net.core.rmem_max"])
		if err != nil {
			t.Fatalf("rmem_max not numeric: %v", err)
		}
		if got <= stockDefault {
			t.Fatalf("rmem_max %d does not beat the stock default %d", got, stockDefault)
		}
		// quic-go asks for ~7.5 MB; anything below that still logs a warning.
		if got < 7_500_000 {
			t.Fatalf("rmem_max %d is below quic-go's ask", got)
		}
	}
}

func TestTunedSysctlsCoverBothTransports(t *testing.T) {
	got := tunedSysctls(2468 * 1024)
	// hysteria2 is QUIC/UDP; Reality is TCP. Both legs need their knob set or
	// the baseline only helps half the traffic.
	for _, k := range []string{
		"net.core.rmem_max", "net.core.wmem_max", "net.ipv4.udp_rmem_min", // UDP/QUIC
		"net.ipv4.tcp_congestion_control", "net.core.default_qdisc", // TCP
	} {
		if got[k] == "" {
			t.Fatalf("baseline is missing %s", k)
		}
	}
	if got["net.ipv4.tcp_congestion_control"] != "bbr" || got["net.core.default_qdisc"] != "fq" {
		t.Fatalf("want bbr+fq, got %s+%s", got["net.ipv4.tcp_congestion_control"], got["net.core.default_qdisc"])
	}
}

func TestNormalizeSysctl(t *testing.T) {
	cases := map[string]string{
		"16777216\n":            "16777216",
		"4096\t87380\t16777216": "4096 87380 16777216",
		"  bbr  \n":             "bbr",
		"":                      "",
	}
	for in, want := range cases {
		if got := normalizeSysctl(in); got != want {
			t.Fatalf("normalizeSysctl(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSortedKeysIsDeterministic(t *testing.T) {
	// The drop-in is regenerated on every setup; unstable ordering would make
	// every run look like a change.
	first := sortedKeys(tunedSysctls(0))
	for i := 0; i < 5; i++ {
		next := sortedKeys(tunedSysctls(0))
		for j := range first {
			if first[j] != next[j] {
				t.Fatalf("key order is not stable: %v vs %v", first, next)
			}
		}
	}
	for i := 1; i < len(first); i++ {
		if first[i-1] >= first[i] {
			t.Fatalf("keys not sorted at %d: %q >= %q", i, first[i-1], first[i])
		}
	}
}

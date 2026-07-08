package handler

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

// appendStat encodes a Stat{name=1,value=2} nested inside a
// QueryStatsResponse{stat=1}, mimicking sing-box's wire output.
func appendStat(b []byte, s stat) []byte {
	var inner []byte
	inner = protowire.AppendTag(inner, 1, protowire.BytesType)
	inner = protowire.AppendString(inner, s.name)
	inner = protowire.AppendTag(inner, 2, protowire.VarintType)
	inner = protowire.AppendVarint(inner, uint64(s.value))

	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendBytes(b, inner)
	return b
}

func TestParseUserStatName(t *testing.T) {
	cases := []struct {
		in         string
		user, kind string
		ok         bool
	}{
		{"user>>>alice>>>traffic>>>uplink", "alice", "uplink", true},
		{"user>>>bob>>>traffic>>>downlink", "bob", "downlink", true},
		{"inbound>>>hy2>>>traffic>>>uplink", "", "", false},
		{"user>>>alice>>>traffic", "", "", false},
		{"garbage", "", "", false},
	}
	for _, c := range cases {
		user, kind, ok := parseUserStatName(c.in)
		if ok != c.ok || user != c.user || kind != c.kind {
			t.Errorf("parseUserStatName(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.in, user, kind, ok, c.user, c.kind, c.ok)
		}
	}
}

func TestStatsCodecRoundTrip(t *testing.T) {
	c := statsCodec{}

	// Request marshals pattern + reset.
	reqBytes, err := c.Marshal(&queryStatsRequest{pattern: statsUserPrefix, reset_: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(reqBytes) == 0 {
		t.Fatal("marshalled request is empty")
	}

	// Build a response the way sing-box would: repeated Stat{name,value}.
	want := []stat{
		{"user>>>alice>>>traffic>>>uplink", 100},
		{"user>>>alice>>>traffic>>>downlink", 2000},
	}
	var payload []byte
	for _, s := range want {
		payload = appendStat(payload, s)
	}

	var resp queryStatsResponse
	if err := c.Unmarshal(payload, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.stats) != len(want) {
		t.Fatalf("got %d stats, want %d", len(resp.stats), len(want))
	}
	for i, s := range resp.stats {
		if s.name != want[i].name || s.value != want[i].value {
			t.Errorf("stat[%d] = %+v, want %+v", i, s, want[i])
		}
	}
}

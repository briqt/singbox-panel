package model

import (
	"math"
	"reflect"
	"testing"
)

func TestAccountedBytes(t *testing.T) {
	for _, tc := range []struct{ raw, want int64 }{
		{0, 0}, {1, 2}, {123456789, 246913578},
		{math.MaxInt64 / 2, math.MaxInt64 - 1},
		{math.MaxInt64/2 + 1, math.MaxInt64},
	} {
		if got := AccountedBytes(tc.raw); got != tc.want {
			t.Fatalf("AccountedBytes(%d)=%d want=%d", tc.raw, got, tc.want)
		}
	}
}

func TestAccountedQuotaMatchesEveryAuthorizationPath(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    int64
		limit  int64
		active bool
	}{
		{"unlimited", 100, 0, true},
		{"below-even-limit", 49, 100, true},
		{"at-even-limit", 50, 100, false},
		{"below-odd-limit", 50, 101, true},
		{"above-odd-limit", 51, 101, false},
		{"one-byte-unused", 0, 1, true},
		{"one-byte-exhausted", 1, 1, false},
		{"large-below", math.MaxInt64 / 2, math.MaxInt64, true},
		{"large-above", math.MaxInt64/2 + 1, math.MaxInt64, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInboundAccessFixture(t)
			if err := f.access.GrantInbound(f.user.ID, f.first.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.database.Exec(`UPDATE users SET traffic_used_bytes=?, traffic_limit_bytes=? WHERE id=?`, tc.raw, tc.limit, f.user.ID); err != nil {
				t.Fatal(err)
			}
			u, err := f.users.Get(f.user.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := f.users.IsActive(u); got != tc.active {
				t.Fatalf("IsActive(raw=%d,limit=%d)=%v want=%v", tc.raw, tc.limit, got, tc.active)
			}
			want := []int{}
			if tc.active {
				want = []int{u.ID}
			}
			for name, query := range map[string]func() ([]int, error){
				"inbound": func() ([]int, error) { return f.access.UsersForInbound(f.first.ID) },
				"node":    func() ([]int, error) { return f.access.UsersForNode(f.node.ID) },
			} {
				got, err := query()
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("%s users=%v err=%v want=%v", name, got, err, want)
				}
			}
		})
	}
}

func TestTrafficAccountingNeverRewritesRawCounters(t *testing.T) {
	f := newInboundAccessFixture(t)
	f.users.AddTraffic(f.user.ID, 123, 456)
	for i := 0; i < 3; i++ {
		u, err := f.users.Get(f.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		if u.TrafficUsedBytes != 579 || u.TrafficUpBytes != 123 || u.TrafficDownBytes != 456 {
			t.Fatalf("raw counters were rewritten: %+v", u)
		}
		if AccountedBytes(u.TrafficUsedBytes) != 1158 {
			t.Fatal("repeated reads must remain x2, never x4")
		}
	}
	if err := f.users.ResetTraffic(f.user.ID); err != nil {
		t.Fatal(err)
	}
	f.users.AddTraffic(f.user.ID, 7, 11)
	u, err := f.users.Get(f.user.ID)
	if err != nil || u.TrafficUsedBytes != 18 || AccountedBytes(u.TrafficUsedBytes) != 36 {
		t.Fatalf("reset/new increments must retain raw semantics: user=%+v err=%v", u, err)
	}
}

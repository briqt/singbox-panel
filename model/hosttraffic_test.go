package model

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func hostStore(t *testing.T) *HostTrafficStore {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE host_traffic_hourly (node_id INTEGER, hour INTEGER, kind TEXT, name TEXT, rx INTEGER, tx INTEGER, rx_packets INTEGER, tx_packets INTEGER, resets INTEGER, PRIMARY KEY (node_id, hour, kind, name))`,
		`CREATE TABLE host_traffic_hours (node_id INTEGER, hour INTEGER, samples INTEGER, max_gap REAL, rx_other_bytes INTEGER, rx_other_count INTEGER, tx_other_bytes INTEGER, tx_other_count INTEGER, PRIMARY KEY (node_id, hour))`,
		`CREATE TABLE host_traffic_peers (node_id INTEGER, hour INTEGER, direction TEXT, address TEXT, bytes INTEGER, packets INTEGER, PRIMARY KEY (node_id, hour, direction, address))`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return &HostTrafficStore{DB: db}
}

func sampleHour(hour int64) HostTrafficHour {
	h := HostTrafficHour{Hour: hour, Samples: 60, MaxGap: 61}
	h.Items = []HostTrafficItem{
		{Kind: "interface", Name: "nic", RX: 1000, TX: 3000},
		{Kind: "service", Name: "sing-box.service", RX: 800, TX: 2500},
		{Kind: "port", Name: "tcp/9 idle", RX: 0, TX: 0},
	}
	h.Peers.RX = []HostTrafficPeer{{Address: "198.51.100.1", Bytes: 700, Packets: 7}}
	h.Peers.RXOther = HostTrafficOther{Count: 3, Bytes: 30}
	return h
}

// A retried pull of the same hour must replace, never add: double counting is
// exactly the error a provider-bill comparison cannot survive.
func TestSaveHoursIsIdempotent(t *testing.T) {
	s := hostStore(t)
	const hour = 1_800_000_000 / 3600 * 3600
	for i := 0; i < 3; i++ {
		if err := s.SaveHours(1, []HostTrafficHour{sampleHour(hour)}, 0); err != nil {
			t.Fatal(err)
		}
	}
	sum, err := s.Summary(1, hour, hour+3600, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Series) != 1 || sum.Series[0].NICRX != 1000 || sum.Series[0].NICTX != 3000 {
		t.Fatalf("series after 3 saves = %+v, want one hour of 1000/3000", sum.Series)
	}
	if sum.Series[0].Services["sing-box.service"] != 3300 {
		t.Fatalf("service sum = %v", sum.Series[0].Services)
	}
	if got := sum.TopPeers["rx"]; len(got) != 1 || got[0].Bytes != 700 {
		t.Fatalf("peers = %+v", got)
	}
	if sum.PeerOther["rx"].Bytes != 30 {
		t.Fatalf("peer other = %+v", sum.PeerOther)
	}
	var zero int
	s.DB.QueryRow(`SELECT COUNT(*) FROM host_traffic_hourly WHERE name = 'tcp/9 idle'`).Scan(&zero)
	if zero != 0 {
		t.Fatal("all-zero rows must not be stored")
	}
	if last, _ := s.LastHour(1); last != hour {
		t.Fatalf("LastHour = %d", last)
	}
}

func TestPeersOlderThanCutoffAreNotStoredAndPruned(t *testing.T) {
	s := hostStore(t)
	const old, recent = 3600 * 100, 3600 * 300
	if err := s.SaveHours(1, []HostTrafficHour{sampleHour(old), sampleHour(recent)}, 3600*200); err != nil {
		t.Fatal(err)
	}
	var n int
	s.DB.QueryRow(`SELECT COUNT(*) FROM host_traffic_peers WHERE hour = ?`, old).Scan(&n)
	if n != 0 {
		t.Fatal("peer detail beyond the peer window was stored")
	}
	if _, err := s.Prune(3600*200, 3600*400); err != nil {
		t.Fatal(err)
	}
	s.DB.QueryRow(`SELECT COUNT(*) FROM host_traffic_hours WHERE hour = ?`, old).Scan(&n)
	if n != 0 {
		t.Fatal("hourly conclusion older than retention survived prune")
	}
	s.DB.QueryRow(`SELECT COUNT(*) FROM host_traffic_hours WHERE hour = ?`, recent).Scan(&n)
	if n != 1 {
		t.Fatal("prune removed an hour inside retention")
	}
	s.DB.QueryRow(`SELECT COUNT(*) FROM host_traffic_peers`).Scan(&n)
	if n != 0 {
		t.Fatal("peers older than their own window survived prune")
	}
}

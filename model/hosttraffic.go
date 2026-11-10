package model

import (
	"database/sql"
	"sort"
	"time"
)

// HostTrafficStore keeps per-hour conclusions about a node's whole-machine
// traffic: NIC totals, per-service and per-port counters, and the busiest public
// peers. The node keeps only a 48h minute-level buffer; the panel is where these
// conclusions live, under the same retention window as proxy usage.
//
// Peer rows are the bulky part (up to 50 per hour per direction per node), so
// they get a much shorter window of their own.
type HostTrafficStore struct {
	DB *sql.DB
}

// HostPeerRetention bounds the per-address detail. Hourly conclusions follow
// TrafficStore.RetentionStart instead.
const HostPeerRetention = 7 * 24 * time.Hour

// HostTrafficItem is one counter's sum within one hour.
type HostTrafficItem struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	RX        int64  `json:"rx"`
	TX        int64  `json:"tx"`
	RXPackets int64  `json:"rx_packets"`
	TXPackets int64  `json:"tx_packets"`
	Resets    int64  `json:"resets"`
}

type HostTrafficPeer struct {
	Address string `json:"address"`
	Bytes   int64  `json:"bytes"`
	Packets int64  `json:"packets"`
}

type HostTrafficOther struct {
	Count   int64 `json:"count"`
	Bytes   int64 `json:"bytes"`
	Packets int64 `json:"packets"`
}

// HostTrafficHour is the node's `traffic-audit.py hourly` output for one hour.
type HostTrafficHour struct {
	Hour    int64             `json:"hour"`
	Samples int64             `json:"samples"`
	MaxGap  float64           `json:"max_gap"`
	Items   []HostTrafficItem `json:"items"`
	Peers   struct {
		RX      []HostTrafficPeer `json:"rx"`
		TX      []HostTrafficPeer `json:"tx"`
		RXOther HostTrafficOther  `json:"rx_other"`
		TXOther HostTrafficOther  `json:"tx_other"`
	} `json:"peers"`
}

// SaveHours replaces every given hour for the node. The node only reports hours
// that have ended, so re-fetching an hour yields the same numbers and replacing
// is idempotent — a retried pull never double counts.
func (s *HostTrafficStore) SaveHours(nodeID int, hours []HostTrafficHour, peerCutoff int64) error {
	if len(hours) == 0 {
		return nil
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, h := range hours {
		for _, table := range []string{"host_traffic_hourly", "host_traffic_hours", "host_traffic_peers"} {
			if _, err := tx.Exec(`DELETE FROM `+table+` WHERE node_id = ? AND hour = ?`, nodeID, h.Hour); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`INSERT INTO host_traffic_hours (node_id, hour, samples, max_gap, rx_other_bytes, rx_other_count, tx_other_bytes, tx_other_count)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, nodeID, h.Hour, h.Samples, h.MaxGap,
			h.Peers.RXOther.Bytes, h.Peers.RXOther.Count, h.Peers.TXOther.Bytes, h.Peers.TXOther.Count); err != nil {
			return err
		}
		for _, item := range h.Items {
			// Zero rows carry no information and would dominate the table: most
			// ports are idle most hours.
			if item.RX == 0 && item.TX == 0 && item.RXPackets == 0 && item.TXPackets == 0 && item.Resets == 0 {
				continue
			}
			if _, err := tx.Exec(`INSERT INTO host_traffic_hourly (node_id, hour, kind, name, rx, tx, rx_packets, tx_packets, resets)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, nodeID, h.Hour, item.Kind, item.Name,
				item.RX, item.TX, item.RXPackets, item.TXPackets, item.Resets); err != nil {
				return err
			}
		}
		if h.Hour < peerCutoff {
			continue
		}
		for direction, peers := range map[string][]HostTrafficPeer{"rx": h.Peers.RX, "tx": h.Peers.TX} {
			for _, p := range peers {
				if _, err := tx.Exec(`INSERT INTO host_traffic_peers (node_id, hour, direction, address, bytes, packets)
					VALUES (?, ?, ?, ?, ?, ?)`, nodeID, h.Hour, direction, p.Address, p.Bytes, p.Packets); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}

// LastHour is the newest hour stored for the node, or 0 when none is.
func (s *HostTrafficStore) LastHour(nodeID int) (int64, error) {
	var hour sql.NullInt64
	err := s.DB.QueryRow(`SELECT MAX(hour) FROM host_traffic_hours WHERE node_id = ?`, nodeID).Scan(&hour)
	return hour.Int64, err
}

// Prune drops hourly conclusions before hourCutoff and peer detail before
// peerCutoff (both Unix seconds).
func (s *HostTrafficStore) Prune(hourCutoff, peerCutoff int64) (int64, error) {
	var total int64
	for _, q := range []struct {
		sql    string
		cutoff int64
	}{
		{`DELETE FROM host_traffic_hourly WHERE hour < ?`, hourCutoff},
		{`DELETE FROM host_traffic_hours WHERE hour < ?`, hourCutoff},
		{`DELETE FROM host_traffic_peers WHERE hour < ?`, peerCutoff},
	} {
		res, err := s.DB.Exec(q.sql, q.cutoff)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// HostTrafficSeriesPoint is one hour of the headline numbers.
type HostTrafficSeriesPoint struct {
	Hour     int64            `json:"hour"`
	NICRX    int64            `json:"nic_rx"`
	NICTX    int64            `json:"nic_tx"`
	WANRX    int64            `json:"wan_rx"`
	WANTX    int64            `json:"wan_tx"`
	Samples  int64            `json:"samples"`
	MaxGap   float64          `json:"max_gap"`
	Resets   int64            `json:"resets"`
	Services map[string]int64 `json:"services"` // unit -> rx+tx this hour
}

type HostTrafficSummary struct {
	NodeID    int                          `json:"node_id"`
	From      int64                        `json:"from"`
	To        int64                        `json:"to"`
	Hours     int64                        `json:"hours"`    // hours with data
	Expected  int64                        `json:"expected"` // hours in [from,to)
	Samples   int64                        `json:"samples"`  // minute samples
	MaxGap    float64                      `json:"max_gap"`  // largest sampling gap seen
	Resets    int64                        `json:"resets"`   // counter resets (lower-bound intervals)
	Totals    []HostTrafficItem            `json:"totals"`   // per kind/name, summed
	Series    []HostTrafficSeriesPoint     `json:"series"`
	PeersFrom int64                        `json:"peers_from"` // peer detail only exists from here
	TopPeers  map[string][]HostTrafficPeer `json:"top_peers"`
	PeerOther map[string]HostTrafficOther  `json:"peer_other"`
}

// Summary aggregates [from, to) for one node.
func (s *HostTrafficStore) Summary(nodeID int, from, to, peerCutoff int64) (*HostTrafficSummary, error) {
	out := &HostTrafficSummary{NodeID: nodeID, From: from, To: to, Expected: (to - from) / 3600,
		TopPeers: map[string][]HostTrafficPeer{"rx": {}, "tx": {}}, PeerOther: map[string]HostTrafficOther{}}
	series := map[int64]*HostTrafficSeriesPoint{}
	point := func(hour int64) *HostTrafficSeriesPoint {
		p, ok := series[hour]
		if !ok {
			p = &HostTrafficSeriesPoint{Hour: hour, Services: map[string]int64{}}
			series[hour] = p
		}
		return p
	}

	rows, err := s.DB.Query(`SELECT hour, samples, max_gap, rx_other_bytes, rx_other_count, tx_other_bytes, tx_other_count
		FROM host_traffic_hours WHERE node_id = ? AND hour >= ? AND hour < ?`, nodeID, from, to)
	if err != nil {
		return nil, err
	}
	rxOther, txOther := HostTrafficOther{}, HostTrafficOther{}
	for rows.Next() {
		var hour, samples, rxb, rxc, txb, txc int64
		var gap float64
		if err := rows.Scan(&hour, &samples, &gap, &rxb, &rxc, &txb, &txc); err != nil {
			rows.Close()
			return nil, err
		}
		p := point(hour)
		p.Samples, p.MaxGap = samples, gap
		out.Hours++
		out.Samples += samples
		if gap > out.MaxGap {
			out.MaxGap = gap
		}
		if hour >= peerCutoff {
			rxOther.Bytes += rxb
			rxOther.Count += rxc
			txOther.Bytes += txb
			txOther.Count += txc
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.DB.Query(`SELECT hour, kind, name, rx, tx, rx_packets, tx_packets, resets
		FROM host_traffic_hourly WHERE node_id = ? AND hour >= ? AND hour < ?`, nodeID, from, to)
	if err != nil {
		return nil, err
	}
	totals := map[[2]string]*HostTrafficItem{}
	for rows.Next() {
		var hour int64
		var it HostTrafficItem
		if err := rows.Scan(&hour, &it.Kind, &it.Name, &it.RX, &it.TX, &it.RXPackets, &it.TXPackets, &it.Resets); err != nil {
			rows.Close()
			return nil, err
		}
		key := [2]string{it.Kind, it.Name}
		t, ok := totals[key]
		if !ok {
			t = &HostTrafficItem{Kind: it.Kind, Name: it.Name}
			totals[key] = t
		}
		t.RX += it.RX
		t.TX += it.TX
		t.RXPackets += it.RXPackets
		t.TXPackets += it.TXPackets
		t.Resets += it.Resets
		out.Resets += it.Resets
		p := point(hour)
		p.Resets += it.Resets
		switch it.Kind {
		case "interface":
			p.NICRX += it.RX
			p.NICTX += it.TX
		case "wan":
			p.WANRX += it.RX
			p.WANTX += it.TX
		case "service":
			p.Services[it.Name] += it.RX + it.TX
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, t := range totals {
		out.Totals = append(out.Totals, *t)
	}
	sort.Slice(out.Totals, func(i, j int) bool {
		a, b := out.Totals[i], out.Totals[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.RX+a.TX != b.RX+b.TX {
			return a.RX+a.TX > b.RX+b.TX
		}
		return a.Name < b.Name
	})
	for _, p := range series {
		out.Series = append(out.Series, *p)
	}
	sort.Slice(out.Series, func(i, j int) bool { return out.Series[i].Hour < out.Series[j].Hour })

	peerFrom := from
	if peerFrom < peerCutoff {
		peerFrom = peerCutoff
	}
	out.PeersFrom = peerFrom
	for _, direction := range []string{"rx", "tx"} {
		rows, err := s.DB.Query(`SELECT address, SUM(bytes), SUM(packets) FROM host_traffic_peers
			WHERE node_id = ? AND direction = ? AND hour >= ? AND hour < ?
			GROUP BY address ORDER BY SUM(bytes) DESC LIMIT 50`, nodeID, direction, peerFrom, to)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p HostTrafficPeer
			if err := rows.Scan(&p.Address, &p.Bytes, &p.Packets); err != nil {
				rows.Close()
				return nil, err
			}
			out.TopPeers[direction] = append(out.TopPeers[direction], p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	out.PeerOther["rx"] = rxOther
	out.PeerOther["tx"] = txOther
	return out, nil
}

// Coverage lists, per node, the newest stored hour. Used by the stats page to
// show which nodes report at all.
func (s *HostTrafficStore) Coverage() (map[int]int64, error) {
	rows, err := s.DB.Query(`SELECT node_id, MAX(hour) FROM host_traffic_hours GROUP BY node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]int64{}
	for rows.Next() {
		var id int
		var hour int64
		if err := rows.Scan(&id, &hour); err != nil {
			return nil, err
		}
		out[id] = hour
	}
	return out, rows.Err()
}

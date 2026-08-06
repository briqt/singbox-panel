package model

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// traffic_logs is the single source of truth for usage: every poll appends the
// delta it observed for one (node, user) pair, and every statistic is an
// aggregation over those rows. Samples are stored in UTC; Loc decides which
// calendar day a sample belongs to and where the retention window starts.
type TrafficStore struct {
	DB  *sql.DB
	Loc *time.Location
}

// RetentionMonths keeps the current calendar month plus the two before it.
const RetentionMonths = 3

const dayLayout = "2006-01-02"

func (s *TrafficStore) Record(nodeID, userID int, up, down int64) {
	s.DB.Exec(`INSERT INTO traffic_logs (node_id, user_id, bytes_up, bytes_down) VALUES (?, ?, ?, ?)`,
		nodeID, userID, up, down)
}

// Today is the current day in panel time; all range boundaries are expressed
// in these day strings, never in UTC.
func (s *TrafficStore) Today() string {
	return time.Now().In(s.location()).Format(dayLayout)
}

// RetentionStart is the first day still kept: the first of the month
// RetentionMonths-1 months back.
func (s *TrafficStore) RetentionStart() string {
	now := time.Now().In(s.location())
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	return first.AddDate(0, -(RetentionMonths - 1), 0).Format(dayLayout)
}

// Prune drops samples older than the retention window. The cutoff day is
// converted to its UTC instant so the delete uses the recorded_at indexes.
func (s *TrafficStore) Prune() (int64, error) {
	cutoff, err := time.ParseInLocation(dayLayout, s.RetentionStart(), s.location())
	if err != nil {
		return 0, err
	}
	res, err := s.DB.Exec(`DELETE FROM traffic_logs WHERE recorded_at < ?`,
		cutoff.UTC().Format(time.DateTime))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// UsageQuery selects a closed day range [From, To] and the dimensions to group
// by. No dimension means a single total row. UserID/NodeID 0 means no filter.
type UsageQuery struct {
	From    string
	To      string
	UserID  int
	NodeID  int
	ByDay   bool
	ByUser  bool
	ByNode  bool
	MaxRows int
}

// UsageRow carries only the grouped dimensions; omitted ones stay zero.
// Names are resolved by join, so rows survive a deleted user or node.
type UsageRow struct {
	Day    string `json:"day,omitempty"`
	UserID int    `json:"user_id,omitempty"`
	User   string `json:"user,omitempty"`
	NodeID int    `json:"node_id,omitempty"`
	Node   string `json:"node,omitempty"`
	Up     int64  `json:"up"`
	Down   int64  `json:"down"`
}

func (s *TrafficStore) Usage(q UsageQuery) ([]UsageRow, error) {
	dayExpr := "date(t.recorded_at, ?)"
	offset := s.offsetModifier()
	selects := []string{}
	groups := []string{}
	args := []any{}

	if q.ByDay {
		selects = append(selects, dayExpr+" AS day")
		groups = append(groups, "day")
		args = append(args, offset)
	}
	if q.ByUser {
		selects = append(selects, "t.user_id", "COALESCE(u.name, '')")
		groups = append(groups, "t.user_id")
	}
	if q.ByNode {
		selects = append(selects, "t.node_id", "COALESCE(n.name, '')")
		groups = append(groups, "t.node_id")
	}
	selects = append(selects, "COALESCE(SUM(t.bytes_up),0)", "COALESCE(SUM(t.bytes_down),0)")

	query := `SELECT ` + strings.Join(selects, ", ") + `
		FROM traffic_logs t
		LEFT JOIN users u ON u.id = t.user_id
		LEFT JOIN nodes n ON n.id = t.node_id
		WHERE ` + dayExpr + ` BETWEEN ? AND ?`
	args = append(args, offset, q.From, q.To)
	if q.UserID > 0 {
		query += " AND t.user_id = ?"
		args = append(args, q.UserID)
	}
	if q.NodeID > 0 {
		query += " AND t.node_id = ?"
		args = append(args, q.NodeID)
	}
	if len(groups) > 0 {
		query += " GROUP BY " + strings.Join(groups, ", ")
		if q.ByDay {
			query += " ORDER BY day, SUM(t.bytes_up) + SUM(t.bytes_down) DESC"
		} else {
			query += " ORDER BY SUM(t.bytes_up) + SUM(t.bytes_down) DESC"
		}
	}
	if q.MaxRows > 0 {
		query += fmt.Sprintf(" LIMIT %d", q.MaxRows)
	}

	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []UsageRow{}
	for rows.Next() {
		var r UsageRow
		targets := []any{}
		if q.ByDay {
			targets = append(targets, &r.Day)
		}
		if q.ByUser {
			targets = append(targets, &r.UserID, &r.User)
		}
		if q.ByNode {
			targets = append(targets, &r.NodeID, &r.Node)
		}
		targets = append(targets, &r.Up, &r.Down)
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		if q.ByUser && r.User == "" {
			r.User = fmt.Sprintf("#%d", r.UserID)
		}
		if q.ByNode && r.Node == "" {
			r.Node = fmt.Sprintf("#%d", r.NodeID)
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// NodeTotals sums the whole retained window per node in one query.
func (s *TrafficStore) NodeTotals() (map[int][2]int64, error) {
	rows, err := s.DB.Query(`SELECT node_id, COALESCE(SUM(bytes_up),0), COALESCE(SUM(bytes_down),0) FROM traffic_logs GROUP BY node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	totals := map[int][2]int64{}
	for rows.Next() {
		var id int
		var up, down int64
		if err := rows.Scan(&id, &up, &down); err != nil {
			return nil, err
		}
		totals[id] = [2]int64{up, down}
	}
	return totals, rows.Err()
}

func (s *TrafficStore) LocationName() string {
	return s.location().String()
}

func (s *TrafficStore) location() *time.Location {
	if s.Loc == nil {
		return time.UTC
	}
	return s.Loc
}

// offsetModifier turns the panel time zone into a SQLite datetime modifier so
// UTC-stored samples fall into the right local day.
func (s *TrafficStore) offsetModifier() string {
	_, offset := time.Now().In(s.location()).Zone()
	return fmt.Sprintf("%+d seconds", offset)
}

package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	paneldb "github.com/briqt/singbox-panel/db"
	"github.com/briqt/singbox-panel/model"
)

// shanghai keeps the tests independent of the machine's own time zone.
var shanghai = mustLoad("Asia/Shanghai")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

type statsTestEnv struct {
	db      *sql.DB
	users   *model.UserStore
	nodes   *model.NodeStore
	traffic *model.TrafficStore
	handler *StatsHandler
}

func newStatsTestEnv(t *testing.T) *statsTestEnv {
	t.Helper()
	database, err := paneldb.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	env := &statsTestEnv{
		db:      database,
		users:   &model.UserStore{DB: database},
		nodes:   &model.NodeStore{DB: database},
		traffic: &model.TrafficStore{DB: database, Loc: shanghai},
	}
	env.handler = &StatsHandler{Users: env.users, Nodes: env.nodes, Traffic: env.traffic}
	return env
}

func (e *statsTestEnv) user(t *testing.T, name string) int {
	t.Helper()
	u, err := e.users.Create(model.CreateUserReq{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func (e *statsTestEnv) node(t *testing.T, name string) int {
	t.Helper()
	n, err := e.nodes.Create(model.CreateNodeReq{Name: name, Host: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	return n.ID
}

// sample writes one poll sample at an explicit UTC instant, which is how the
// poller stores them.
func (e *statsTestEnv) sample(t *testing.T, nodeID, userID int, recordedUTC string, up, down int64) {
	t.Helper()
	_, err := e.db.Exec(`INSERT INTO traffic_logs (node_id, user_id, bytes_up, bytes_down, recorded_at) VALUES (?, ?, ?, ?, ?)`,
		nodeID, userID, up, down, recordedUTC)
	if err != nil {
		t.Fatal(err)
	}
}

// day returns a day inside the retention window, offset from today.
func (e *statsTestEnv) day(offset int) string {
	return time.Now().In(shanghai).AddDate(0, 0, offset).Format("2006-01-02")
}

// at converts a wall-clock moment of a panel day into the UTC instant stored
// in traffic_logs.
func at(t *testing.T, day string, hour, minute int) string {
	t.Helper()
	local, err := time.ParseInLocation("2006-01-02", day, shanghai)
	if err != nil {
		t.Fatal(err)
	}
	return local.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute).
		UTC().Format(time.DateTime)
}

func (e *statsTestEnv) usage(t *testing.T, query string) []model.UsageRow {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/stats/usage?"+query, nil)
	rec := httptest.NewRecorder()
	e.handler.HandleUsage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage %q: status %d body %s", query, rec.Code, rec.Body.String())
	}
	var rows []model.UsageRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestUsageBreaksDownByUserDayAndNode(t *testing.T) {
	env := newStatsTestEnv(t)
	alice := env.user(t, "alice")
	bob := env.user(t, "bob")
	tokyo := env.node(t, "tokyo")
	lax := env.node(t, "lax")

	yesterday, today := env.day(-1), env.day(0)
	env.sample(t, tokyo, alice, at(t, yesterday, 0, 30), 100, 900)
	env.sample(t, lax, alice, at(t, yesterday, 4, 0), 10, 90)
	env.sample(t, tokyo, alice, at(t, today, 1, 0), 1, 2)
	env.sample(t, tokyo, bob, at(t, yesterday, 1, 0), 5, 5)

	rows := env.usage(t, "from="+yesterday+"&to="+today+"&group=day,user,node")
	want := []model.UsageRow{
		{Day: yesterday, UserID: alice, User: "alice", NodeID: tokyo, Node: "tokyo", Up: 100, Down: 900},
		{Day: yesterday, UserID: alice, User: "alice", NodeID: lax, Node: "lax", Up: 10, Down: 90},
		{Day: yesterday, UserID: bob, User: "bob", NodeID: tokyo, Node: "tokyo", Up: 5, Down: 5},
		{Day: today, UserID: alice, User: "alice", NodeID: tokyo, Node: "tokyo", Up: 1, Down: 2},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Fatalf("row %d: got %+v, want %+v", i, rows[i], want[i])
		}
	}
}

// A sample taken at 16:30 UTC belongs to the next day in Shanghai; grouping
// must follow the panel's calendar, not the storage calendar.
func TestUsageDayFollowsPanelTimezone(t *testing.T) {
	env := newStatsTestEnv(t)
	alice := env.user(t, "alice")
	tokyo := env.node(t, "tokyo")
	yesterday, today := env.day(-1), env.day(0)
	// 00:30 in Shanghai is 16:30 UTC of the previous day.
	env.sample(t, tokyo, alice, at(t, today, 0, 30), 7, 3)

	if rows := env.usage(t, "from="+yesterday+"&to="+yesterday+"&group=day"); len(rows) != 0 {
		t.Fatalf("sample leaked into the UTC day: %+v", rows)
	}
	rows := env.usage(t, "from="+today+"&to="+today+"&group=day")
	if len(rows) != 1 || rows[0].Day != today || rows[0].Up != 7 || rows[0].Down != 3 {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}

func TestUsageWithoutGroupReturnsOneTotal(t *testing.T) {
	env := newStatsTestEnv(t)
	alice := env.user(t, "alice")
	tokyo := env.node(t, "tokyo")
	today := env.day(0)
	env.sample(t, tokyo, alice, at(t, today, 9, 0), 4, 6)
	env.sample(t, tokyo, alice, at(t, today, 10, 0), 1, 1)

	rows := env.usage(t, "from="+today+"&to="+today)
	if len(rows) != 1 || rows[0].Up != 5 || rows[0].Down != 7 || rows[0].Day != "" {
		t.Fatalf("unexpected total: %+v", rows)
	}
}

func TestUsageRejectsUnknownGroupAndBadRange(t *testing.T) {
	env := newStatsTestEnv(t)
	queries := []string{
		"group=protocol",
		"from=" + env.day(0) + "&to=" + env.day(-1),
		"from=03/01/2026",
		"to=2000-01-01", // wholly outside the retained window
	}
	for _, query := range queries {
		req := httptest.NewRequest(http.MethodGet, "/api/stats/usage?"+query, nil)
		rec := httptest.NewRecorder()
		env.handler.HandleUsage(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("query %q: got status %d, want 400", query, rec.Code)
		}
	}
}

// A request reaching past the retention window is clamped instead of being
// answered with rows the panel no longer keeps.
func TestUsageClampsRangeToRetentionWindow(t *testing.T) {
	env := newStatsTestEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/api/stats/usage?from=2000-01-01&group=day", nil)
	q, err := env.handler.parseUsageQuery(req, defaultRangeDays)
	if err != nil {
		t.Fatal(err)
	}
	if q.From != env.traffic.RetentionStart() {
		t.Fatalf("from = %s, want retention start %s", q.From, env.traffic.RetentionStart())
	}
	if q.To != env.traffic.Today() {
		t.Fatalf("to = %s, want today %s", q.To, env.traffic.Today())
	}
}

func TestRetentionStartIsFirstOfThirdCalendarMonth(t *testing.T) {
	env := newStatsTestEnv(t)
	now := time.Now().In(shanghai)
	want := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, shanghai).
		AddDate(0, -(model.RetentionMonths - 1), 0).Format("2006-01-02")
	if got := env.traffic.RetentionStart(); got != want {
		t.Fatalf("retention start = %s, want %s", got, want)
	}
}

func TestPruneDropsOnlySamplesBeforeRetentionStart(t *testing.T) {
	env := newStatsTestEnv(t)
	alice := env.user(t, "alice")
	tokyo := env.node(t, "tokyo")

	start, err := time.ParseInLocation("2006-01-02", env.traffic.RetentionStart(), shanghai)
	if err != nil {
		t.Fatal(err)
	}
	env.sample(t, tokyo, alice, start.Add(-time.Hour).UTC().Format(time.DateTime), 1, 1)
	env.sample(t, tokyo, alice, start.Add(time.Hour).UTC().Format(time.DateTime), 2, 2)

	pruned, err := env.traffic.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Fatalf("pruned %d rows, want 1", pruned)
	}
	var remaining int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM traffic_logs`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("%d rows left, want 1", remaining)
	}
}

func TestMyUsageIsScopedToCaller(t *testing.T) {
	env := newStatsTestEnv(t)
	alice := env.user(t, "alice")
	bob := env.user(t, "bob")
	tokyo := env.node(t, "tokyo")
	today := env.traffic.Today()
	recorded := time.Now().UTC().Format(time.DateTime)
	env.sample(t, tokyo, alice, recorded, 10, 20)
	env.sample(t, tokyo, bob, recorded, 30, 40)

	req := httptest.NewRequest(http.MethodGet, "/api/me/usage?group=day,user,node&from="+today+"&to="+today, nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, alice))
	rec := httptest.NewRecorder()
	env.handler.HandleMyUsage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var rows []model.UsageRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Up != 10 || rows[0].Down != 20 {
		t.Fatalf("caller saw foreign usage: %+v", rows)
	}
	if rows[0].UserID != 0 {
		t.Fatalf("self view should not group by user: %+v", rows[0])
	}
}

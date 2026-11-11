package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/briqt/singbox-panel/model"
)

func TestEveryTrafficResponseAccountsHistoricalRawBytesExactlyOnce(t *testing.T) {
	env := newStatsTestEnv(t)
	userID := env.user(t, "historical")
	nodeID := env.node(t, "entry")
	if _, err := env.db.Exec(`UPDATE users SET traffic_used_bytes=40, traffic_up_bytes=15, traffic_down_bytes=25, traffic_limit_bytes=100, traffic_reset_day=0 WHERE id=?`, userID); err != nil {
		t.Fatal(err)
	}
	day := env.day(-1)
	env.sample(t, nodeID, userID, at(t, day, 12, 0), 15, 25)
	users := &UserHandler{Store: env.users, Access: &model.AccessStore{DB: env.db}}
	me := &MeHandler{Users: env.users}
	for repeat := 0; repeat < 2; repeat++ {
		for _, tc := range []struct {
			name   string
			path   string
			method string
			body   string
			handle http.HandlerFunc
			list   bool
			want   map[string]int64
		}{
			{"users", "/api/users", "GET", "", users.ServeHTTP, true, map[string]int64{"traffic_used_bytes": 80, "traffic_up_bytes": 30, "traffic_down_bytes": 50, "traffic_limit_bytes": 100}},
			{"user", "/api/users/" + strconv.Itoa(userID), "GET", "", users.ServeHTTP, false, map[string]int64{"traffic_used_bytes": 80, "traffic_up_bytes": 30, "traffic_down_bytes": 50, "traffic_limit_bytes": 100}},
			{"update", "/api/users/" + strconv.Itoa(userID), "PUT", `{"note":"accounted"}`, users.ServeHTTP, false, map[string]int64{"traffic_used_bytes": 80, "traffic_up_bytes": 30, "traffic_down_bytes": 50, "traffic_limit_bytes": 100}},
			{"me", "/api/me", "GET", "", me.HandleMe, false, map[string]int64{"traffic_used_bytes": 80, "traffic_limit_bytes": 100}},
			{"user-stats", "/api/stats/users", "GET", "", env.handler.HandleUserStats, true, map[string]int64{"used_bytes": 80, "up_bytes": 30, "down_bytes": 50, "limit_bytes": 100}},
			{"node-stats", "/api/stats/nodes", "GET", "", env.handler.HandleNodeStats, true, map[string]int64{"up_bytes": 30, "down_bytes": 50}},
			{"usage", "/api/stats/usage?from=" + day + "&to=" + day, "GET", "", env.handler.HandleUsage, true, map[string]int64{"up": 30, "down": 50}},
			{"my-usage", "/api/me/usage?from=" + day + "&to=" + day, "GET", "", env.handler.HandleMyUsage, true, map[string]int64{"up": 30, "down": 50}},
		} {
			t.Run(tc.name+strconv.Itoa(repeat), func(t *testing.T) {
				req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
				req = req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
				rec := httptest.NewRecorder()
				tc.handle(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				var object map[string]json.RawMessage
				if tc.list {
					var list []map[string]json.RawMessage
					if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 {
						t.Fatalf("list=%s err=%v", rec.Body.String(), err)
					}
					object = list[0]
				} else if err := json.Unmarshal(rec.Body.Bytes(), &object); err != nil {
					t.Fatal(err)
				}
				for key, want := range tc.want {
					var got int64
					if err := json.Unmarshal(object[key], &got); err != nil || got != want {
						t.Fatalf("%s=%s want=%d err=%v", key, object[key], want, err)
					}
				}
			})
		}
	}
	var used, up, down int64
	if err := env.db.QueryRow(`SELECT traffic_used_bytes,traffic_up_bytes,traffic_down_bytes FROM users WHERE id=?`, userID).Scan(&used, &up, &down); err != nil || used != 40 || up != 15 || down != 25 {
		t.Fatalf("raw user counters changed: %d/%d/%d err=%v", used, up, down, err)
	}
	if err := env.db.QueryRow(`SELECT bytes_up,bytes_down FROM traffic_logs WHERE user_id=?`, userID).Scan(&up, &down); err != nil || up != 15 || down != 25 {
		t.Fatalf("raw historical sample changed: %d/%d err=%v", up, down, err)
	}
	// 新增数据仍存原始值；旧历史与新增量使用同一个折算出口。
	env.users.AddTraffic(userID, 2, 3)
	env.traffic.Record(nodeID, userID, 2, 3)
	current, err := env.users.Get(userID)
	if err != nil || accountUser(*current).TrafficUsedBytes != 90 || current.TrafficUsedBytes != 45 {
		t.Fatalf("new increment was lost or charged twice: user=%+v err=%v", current, err)
	}
}

func TestHostTrafficResponseNeverUsesProxyMultiplier(t *testing.T) {
	env := newStatsTestEnv(t)
	nodeID := env.node(t, "measured")
	hour := time.Now().Unix()/3600*3600 - 3600
	store := &model.HostTrafficStore{DB: env.db}
	err := store.SaveHours(nodeID, []model.HostTrafficHour{{
		Hour: hour, Samples: 60,
		Items: []model.HostTrafficItem{
			{Kind: "interface", Name: "nic", RX: 1000, TX: 3000},
			{Kind: "service", Name: "sing-box.service", RX: 1100, TX: 2200},
		},
	}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	h := &HostAuditHandler{Store: store, Traffic: env.traffic}
	rec := httptest.NewRecorder()
	h.HandleQuery(rec, httptest.NewRequest(http.MethodGet, "/api/host-traffic?node_id="+strconv.Itoa(nodeID)+"&from="+strconv.FormatInt(hour, 10)+"&to="+strconv.FormatInt(hour+3600, 10), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Summary model.HostTrafficSummary `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Summary.Series) != 1 {
		t.Fatalf("unexpected host series: %+v", response.Summary.Series)
	}
	point := response.Summary.Series[0]
	if point.NICRX != 1000 || point.NICTX != 3000 || point.Services["sing-box.service"] != 3300 {
		t.Fatalf("measured host traffic was scaled: %+v", point)
	}
}

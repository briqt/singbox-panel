package handler

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/briqt/singbox-panel/model"
)

func TestSubscriptionReportsAccountedTrafficWithoutRewritingRaw(t *testing.T) {
	env := newHandlerTestEnv(t)
	u, err := env.users.Create(model.CreateUserReq{Name: "accounting", TrafficLimitBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.users.DB.Exec(`UPDATE users SET traffic_reset_day=0 WHERE id=?`, u.ID); err != nil {
		t.Fatal(err)
	}
	env.users.AddTraffic(u.ID, 13, 29)
	h := &SubscriptionHandler{Users: env.users, Nodes: env.nodes, Access: env.access}
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sub/"+u.SubToken, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		fields := map[string]int64{}
		for _, field := range strings.Split(rec.Header().Get("Subscription-Userinfo"), ";") {
			key, value, ok := strings.Cut(strings.TrimSpace(field), "=")
			if !ok {
				t.Fatalf("invalid subscription field: %q", field)
			}
			fields[key], err = strconv.ParseInt(value, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
		}
		for key, want := range map[string]int64{"upload": 26, "download": 58, "total": 1000} {
			if got := fields[key]; got != want {
				t.Fatalf("%s=%d want=%d", key, got, want)
			}
		}
	}
	raw, err := env.users.Get(u.ID)
	if err != nil || raw.TrafficUpBytes != 13 || raw.TrafficDownBytes != 29 || raw.TrafficUsedBytes != 42 {
		t.Fatalf("subscription changed raw counters: user=%+v err=%v", raw, err)
	}
}

func TestSubscriptionRejectsAtAccountedQuota(t *testing.T) {
	env := newHandlerTestEnv(t)
	u, err := env.users.Create(model.CreateUserReq{Name: "exhausted-at-half", TrafficLimitBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.users.DB.Exec(`UPDATE users SET traffic_reset_day=0 WHERE id=?`, u.ID); err != nil {
		t.Fatal(err)
	}
	env.users.AddTraffic(u.ID, 21, 29)
	h := &SubscriptionHandler{Users: env.users, Nodes: env.nodes, Access: env.access}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sub/"+u.SubToken, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("raw 50 must exhaust accounted quota 100: status=%d", rec.Code)
	}
}

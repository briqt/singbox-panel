package handler

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/briqt/singbox-panel/model"
)

func TestUnlimitedSubscriptionUsesOnePiBDisplayTotal(t *testing.T) {
	env := newHandlerTestEnv(t)
	user, err := env.users.Create(model.CreateUserReq{Name: "unlimited"})
	if err != nil {
		t.Fatal(err)
	}

	handler := &SubscriptionHandler{Users: env.users, Nodes: env.nodes, Access: env.access}
	req := httptest.NewRequest(http.MethodGet, "/sub/"+user.SubToken, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	want := "total=" + strconv.FormatInt(unlimitedSubscriptionTotalBytes, 10)
	if got := rec.Header().Get("Subscription-Userinfo"); !strings.Contains(got, want) {
		t.Fatalf("Subscription-Userinfo=%q, want %q", got, want)
	}
}

func TestLimitedSubscriptionKeepsConfiguredTotal(t *testing.T) {
	env := newHandlerTestEnv(t)
	const limit int64 = 100 << 30
	user, err := env.users.Create(model.CreateUserReq{Name: "limited", TrafficLimitBytes: limit})
	if err != nil {
		t.Fatal(err)
	}

	handler := &SubscriptionHandler{Users: env.users, Nodes: env.nodes, Access: env.access}
	req := httptest.NewRequest(http.MethodGet, "/sub/"+user.SubToken, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	want := "total=" + strconv.FormatInt(limit, 10)
	if got := rec.Header().Get("Subscription-Userinfo"); !strings.Contains(got, want) {
		t.Fatalf("Subscription-Userinfo=%q, want %q", got, want)
	}
}

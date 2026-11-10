package handler

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestSubscriptionsFilterEachInboundNotJustNodeOrProtocol(t *testing.T) {
	env := newHandlerTestEnv(t)
	node := env.createNode(t, "shared")
	first := env.createAccessInbound(t, node.ID, "vless-reality", 443)
	second := env.createAccessInbound(t, node.ID, "hysteria2", 8443)
	userA, err := env.users.Create(model.CreateUserReq{Name: "only-A"})
	if err != nil {
		t.Fatal(err)
	}
	userB, err := env.users.Create(model.CreateUserReq{Name: "only-B"})
	if err != nil {
		t.Fatal(err)
	}
	for userID, inboundID := range map[int]int{userA.ID: first.ID, userB.ID: second.ID} {
		if err := env.access.ReplaceInbounds(userID, []int{inboundID}); err != nil {
			t.Fatal(err)
		}
		// 故意保留宽泛的旧节点授权；即便入站属于同一节点，
		// 也不能因此扩大任一用户的精确入站授权。
		if _, err := env.users.DB.Exec(`INSERT INTO user_access (user_id, node_id) VALUES (?, ?)`, userID, node.ID); err != nil {
			t.Fatal(err)
		}
	}
	// 与 A 同节点、同协议，但在授权之后才创建。
	env.createAccessInbound(t, node.ID, "vless-reality", 9443)
	h := &SubscriptionHandler{Users: env.users, Nodes: env.nodes, Access: env.access}
	for _, test := range []struct {
		name          string
		user          *model.User
		inbound       *model.NodeInbound
		scheme        string
		credentialKey string
	}{
		{"A", userA, first, "vless", "uuid"},
		{"B", userB, second, "hysteria2", "password"},
	} {
		for _, format := range []string{"base64", "clash"} {
			t.Run(test.name+"/"+format, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "/sub/"+test.user.SubToken+"?format="+format, nil)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				if format == "base64" {
					plain, err := base64.StdEncoding.DecodeString(rec.Body.String())
					if err != nil {
						t.Fatal(err)
					}
					lines := strings.Split(strings.TrimSpace(string(plain)), "\n")
					if len(lines) != 1 {
						t.Fatalf("unauthorized inbound leaked into URIs: %s", plain)
					}
					uri, err := url.Parse(lines[0])
					if err != nil {
						t.Fatal(err)
					}
					if uri.Scheme != test.scheme || uri.Port() != strconv.Itoa(test.inbound.Port) || uri.User == nil || uri.User.Username() != test.user.UUID {
						t.Fatalf("wrong authorized endpoint or credential: %s", plain)
					}
					return
				}
				proxies := subscriptionClashProxies(t, rec.Body.String())
				if len(proxies) != 1 {
					t.Fatalf("unauthorized inbound leaked into Clash proxies: %v", proxies)
				}
				proxy := proxies[0]
				if proxy["port"] != strconv.Itoa(test.inbound.Port) || proxy["type"] != test.scheme || proxy[test.credentialKey] != test.user.UUID {
					t.Fatalf("wrong authorized Clash endpoint or credential: %v", proxy)
				}
			})
		}
	}
}

// 解析生成 YAML 的精确字段值，不使用 Contains；
// 仅匹配密码或端口的前缀，会让已被改错的值仍然通过测试。
func subscriptionClashProxies(t *testing.T, document string) []map[string]string {
	t.Helper()
	_, section, ok := strings.Cut(document, "\nproxies:\n")
	if !ok {
		t.Fatal("missing proxies section")
	}
	section, _, ok = strings.Cut(section, "\nproxy-groups:\n")
	if !ok {
		t.Fatal("missing proxy-groups boundary")
	}
	var proxies []map[string]string
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- name:") {
			proxies = append(proxies, map[string]string{})
			continue
		}
		key, value, ok := strings.Cut(line, ": ")
		if ok && len(proxies) > 0 {
			proxies[len(proxies)-1][key] = value
		}
	}
	return proxies
}

func TestSubscriptionAuthorizationErrorsFailClosed(t *testing.T) {
	for _, failure := range []string{"access-table", "inbound-decoding"} {
		t.Run(failure, func(t *testing.T) {
			env := newHandlerTestEnv(t)
			node := env.createNode(t, "shared")
			inbound := env.createAccessInbound(t, node.ID, "vless-reality", 443)
			user, err := env.users.Create(model.CreateUserReq{Name: "fail-closed"})
			if err != nil {
				t.Fatal(err)
			}
			if err := env.access.ReplaceInbounds(user.ID, []int{inbound.ID}); err != nil {
				t.Fatal(err)
			}
			if _, err := env.users.DB.Exec(`INSERT INTO user_access (user_id, node_id) VALUES (?, ?)`, user.ID, node.ID); err != nil {
				t.Fatal(err)
			}
			statement := `DROP TABLE user_inbound_access`
			if failure == "inbound-decoding" {
				statement = `UPDATE node_inbounds SET outbound = '{'`
			}
			if _, err := env.users.DB.Exec(statement); err != nil {
				t.Fatal(err)
			}
			h := &SubscriptionHandler{Users: env.users, Nodes: env.nodes, Access: env.access}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sub/"+user.SubToken, nil))
			if rec.Code != http.StatusInternalServerError || rec.Header().Get("Subscription-Userinfo") != "" {
				t.Fatalf("failed authorization/read must not return a successful subscription: status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

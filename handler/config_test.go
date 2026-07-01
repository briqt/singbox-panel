package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRawConfigWriteIsDisabled(t *testing.T) {
	handler := &ConfigHandler{}
	req := httptest.NewRequest(http.MethodPut, "/api/nodes/1/raw-config", strings.NewReader(`{"inbounds":[]}`))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("Allow=%q", rec.Header().Get("Allow"))
	}
	if !strings.Contains(rec.Body.String(), "read-only") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestAtomicDeployCommandRestoresPreviousConfigOnRestartFailure(t *testing.T) {
	command := buildAtomicDeployCommand("/etc/sing-box/config.json", "/tmp/panel-config.json")
	for _, fragment := range []string{
		"cp /etc/sing-box/config.json /tmp/panel-config.json.backup",
		"cp /tmp/panel-config.json /etc/sing-box/config.json",
		"cp /tmp/panel-config.json.backup /etc/sing-box/config.json",
		"systemctl restart sing-box",
	} {
		if !strings.Contains(command, fragment) {
			t.Fatalf("deploy command is missing %q:\n%s", fragment, command)
		}
	}
}

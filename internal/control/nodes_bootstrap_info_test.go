package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNodeBootstrapInfoAPIRequiresAuthentication covers the "GET /api/nodes/bootstrap-info requires a session"
// requirement: it carries no secrets, but should still be unreachable without one, like every other route.
func TestNodeBootstrapInfoAPIRequiresAuthentication(t *testing.T) {
	handler := newTestControl(t).Handler()
	setupAdminForTest(t, handler)
	request := httptest.NewRequest(http.MethodGet, "/api/nodes/bootstrap-info", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated bootstrap-info status=%d, body=%s", response.Code, response.Body.String())
	}
}

// TestNodeBootstrapInfoAPIReportsVersionModeAndSouthbound exercises the fields SetBootstrapInfo feeds through,
// with and without southbound enabled, and confirms "/api/nodes/bootstrap-info" is routed to this handler rather
// than nodeAPI's id-based dispatch (which would otherwise treat "bootstrap-info" as an unknown node id and 404).
func TestNodeBootstrapInfoAPIReportsVersionModeAndSouthbound(t *testing.T) {
	control := newTestControl(t)
	control.SetBootstrapInfo("1.2.3", "controller", ":7443", "127.0.0.1:8080", []string{"console.test"})
	handler := control.Handler()
	cookie := setupAdminForTest(t, handler)
	get := func() nodeBootstrapInfo {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/nodes/bootstrap-info", nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("bootstrap-info status=%d, body=%s", response.Code, response.Body.String())
		}
		var info nodeBootstrapInfo
		if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil {
			t.Fatal(err)
		}
		return info
	}

	info := get()
	if info.Version != "1.2.3" || info.Mode != "controller" {
		t.Fatalf("unexpected version/mode: %#v", info)
	}
	if !info.SouthboundEnabled || info.SouthboundAddr != ":7443" || info.SouthboundPort != 7443 {
		t.Fatalf("expected southbound enabled on port 7443: %#v", info)
	}
	if info.ConsoleAddr != "127.0.0.1:8080" || len(info.ConsoleHostnames) != 1 || info.ConsoleHostnames[0] != "console.test" {
		t.Fatalf("expected consoleAddr/consoleHostnames to be reported verbatim: %#v", info)
	}
	if info.NodeControllerURL != "" || info.NodeImage != "" {
		t.Fatalf("expected empty node settings before any are configured: %#v", info)
	}

	control.SetBootstrapInfo("1.2.3", "all-in-one", "", "127.0.0.1:8080", nil)
	info = get()
	if info.SouthboundEnabled || info.SouthboundAddr != "" || info.SouthboundPort != 0 {
		t.Fatalf("expected southbound disabled: %#v", info)
	}
	if len(info.ConsoleHostnames) != 0 {
		t.Fatalf("expected no console hostname restriction: %#v", info)
	}

	settingsRequest := httptest.NewRequest(http.MethodPut, "/api/settings",
		strings.NewReader(`{"nodeControllerUrl":"https://controller.example.com:7443","nodeImage":"rpop:1.2.3"}`))
	settingsRequest.AddCookie(cookie)
	settingsResponse := httptest.NewRecorder()
	handler.ServeHTTP(settingsResponse, settingsRequest)
	if settingsResponse.Code != http.StatusOK {
		t.Fatalf("update settings status=%d, body=%s", settingsResponse.Code, settingsResponse.Body.String())
	}
	info = get()
	if info.NodeControllerURL != "https://controller.example.com:7443" || info.NodeImage != "rpop:1.2.3" {
		t.Fatalf("expected bootstrap-info to reflect the node settings just saved: %#v", info)
	}
}

// TestNodeBootstrapInfoAPIRejectsOtherMethods keeps the handler symmetric with the other read-only endpoints.
func TestNodeBootstrapInfoAPIRejectsOtherMethods(t *testing.T) {
	handler := newTestControl(t).Handler()
	cookie := setupAdminForTest(t, handler)
	request := httptest.NewRequest(http.MethodPost, "/api/nodes/bootstrap-info", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST bootstrap-info status=%d, want 405: %s", response.Code, response.Body.String())
	}
}

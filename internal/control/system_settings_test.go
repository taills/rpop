package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNormalizeNodeControllerURL covers the "https://host[:port], no path/query/userinfo, IPv6 with brackets"
// contract the node onboarding guide (see nodeBootstrapInfoAPI) relies on to build a working "-controller" flag
// value by trimming the stored value alone.
func TestNormalizeNodeControllerURL(t *testing.T) {
	valid := map[string]string{
		"":                                    "",
		"  ":                                  "",
		"https://controller.example.com:7443": "https://controller.example.com:7443",
		"https://controller.example.com":      "https://controller.example.com",
		"https://[::1]:7443":                  "https://[::1]:7443",
		"https://[::1]":                       "https://[::1]",
		"https://10.0.0.5:7443/":              "https://10.0.0.5:7443",
		"  https://10.0.0.5:7443  ":           "https://10.0.0.5:7443",
	}
	for input, want := range valid {
		got, err := normalizeNodeControllerURL(input)
		if err != nil {
			t.Fatalf("normalizeNodeControllerURL(%q): unexpected error: %v", input, err)
		}
		if got != want {
			t.Fatalf("normalizeNodeControllerURL(%q) = %q, want %q", input, got, want)
		}
	}

	invalid := []string{
		"http://controller.example.com:7443",       // not https
		"controller.example.com:7443",              // missing scheme
		"https://user:pass@controller.example.com", // user info
		"https://controller.example.com/join",      // path
		"https://controller.example.com?token=abc", // query
		"https://controller.example.com#fragment",  // fragment
		"https://controller.example.com:notaport",  // bad port
		"https://controller.example.com:99999",     // out-of-range port
		"https://",                                 // no host
		"not a url at all \x7f",                    // unparsable
	}
	for _, input := range invalid {
		if _, err := normalizeNodeControllerURL(input); err == nil {
			t.Fatalf("normalizeNodeControllerURL(%q): expected an error", input)
		}
	}
}

// TestNormalizeNodeImage covers the "empty (console defaults to rpop:<version>), otherwise no whitespace"
// contract.
func TestNormalizeNodeImage(t *testing.T) {
	valid := map[string]string{
		"":                                     "",
		"   ":                                  "",
		"rpop:1.2.3":                           "rpop:1.2.3",
		"  registry.example.com/rpop  ":        "registry.example.com/rpop",
		"registry.example.com:5000/rpop:1.2.3": "registry.example.com:5000/rpop:1.2.3",
	}
	for input, want := range valid {
		got, err := normalizeNodeImage(input)
		if err != nil {
			t.Fatalf("normalizeNodeImage(%q): unexpected error: %v", input, err)
		}
		if got != want {
			t.Fatalf("normalizeNodeImage(%q) = %q, want %q", input, got, want)
		}
	}

	invalid := []string{"rpop 1.2.3", "rpop:1.2.3\ntag", "rpop\t:1.2.3"}
	for _, input := range invalid {
		if _, err := normalizeNodeImage(input); err == nil {
			t.Fatalf("normalizeNodeImage(%q): expected an error", input)
		}
	}
}

// TestSystemSettingsAPIPersistsNodeControllerURLAndImage exercises the field end-to-end through PUT /api/settings,
// alongside the invalid-input case that must leave the previously stored value untouched (mirrors
// TestSystemSettingsAPIPersistsAndAppliesOneGlobalTimeZone's invalid-timezone case).
func TestSystemSettingsAPIPersistsNodeControllerURLAndImage(t *testing.T) {
	handler := newTestControl(t).Handler()
	cookie := setupAdminForTest(t, handler)
	call := func(method, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, "/api/settings", strings.NewReader(body))
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	decode := func(response *httptest.ResponseRecorder) systemSettings {
		t.Helper()
		var settings systemSettings
		if err := json.Unmarshal(response.Body.Bytes(), &settings); err != nil {
			t.Fatalf("decode settings response: %v (body=%s)", err, response.Body.String())
		}
		return settings
	}

	settings := decode(call(http.MethodGet, ""))
	if settings.NodeControllerURL != "" || settings.NodeImage != "" {
		t.Fatalf("expected empty defaults: %#v", settings)
	}

	response := call(http.MethodPut, `{"nodeControllerUrl":"https://controller.example.com:7443","nodeImage":"rpop:1.2.3"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("update settings status=%d, body=%s", response.Code, response.Body.String())
	}
	settings = decode(response)
	if settings.NodeControllerURL != "https://controller.example.com:7443" || settings.NodeImage != "rpop:1.2.3" {
		t.Fatalf("unexpected settings after update: %#v", settings)
	}

	if response := call(http.MethodPut, `{"nodeControllerUrl":"not-a-url","nodeImage":"rpop:1.2.3"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid nodeControllerUrl to be rejected, got status=%d body=%s", response.Code, response.Body.String())
	}
	settings = decode(call(http.MethodGet, ""))
	if settings.NodeControllerURL != "https://controller.example.com:7443" {
		t.Fatalf("invalid update must not change the stored value: %#v", settings)
	}
}

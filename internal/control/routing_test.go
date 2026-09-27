package control

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/routing"
	"github.com/rpop-project/rpop/internal/store"
)

func newTestControl(t *testing.T) *Control {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	return New(store.New(db), zap.NewNop())
}

func TestSimulateRouteShowsRewrite(t *testing.T) {
	input := routeSimulationRequest{
		Config: store.Config{
			Upstreams: []store.Upstream{{URL: "http://default:8080"}, {URL: "https://user:pass@api.internal/v2"}},
			Routes: []routing.Route{
				{Path: "/api/*", StripPrefix: true, Upstream: 1},
				{Path: "/api/*", Headers: []routing.HeaderMatch{{Name: "Host", Values: []string{"legacy.*"}}}, Upstream: 0},
			},
		},
		URL:     "shop.example.com/API/users/../orders?id=7",
		Headers: []simulationHeader{{Name: "X-Env", Value: "beta"}},
	}
	result, err := simulateRoute(input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched || result.RouteIndex != 0 || result.Upstream != 1 || result.Route != "/api/*" || result.StripPrefix != "/api" {
		t.Fatalf("unexpected match: %#v", result)
	}
	if result.RequestURI != "/API/users/../orders?id=7" || result.MatchPath != "/API/orders" || result.StrippedPath != "/orders" {
		t.Fatalf("unexpected paths: %#v", result)
	}
	if result.UpstreamURI != "/v2/orders?id=7" || result.FinalURL != "https://api.internal/v2/orders?id=7" || result.UpstreamURL != "https://user:xxxxx@api.internal/v2" {
		t.Fatalf("unexpected upstream request: %#v", result)
	}
	if len(result.Checks) != 2 || result.Checks[0].Result != routing.ResultHeaderFailed || result.Checks[1].Result != routing.ResultMatched {
		t.Fatalf("unexpected checks: %#v", result.Checks)
	}

	input.Headers = []simulationHeader{{Name: "host", Value: "legacy.example.com"}}
	result, err = simulateRoute(input)
	if err != nil {
		t.Fatal(err)
	}
	if result.RouteIndex != 1 || result.Upstream != 0 || result.FinalURL != "http://default:8080/API/users/../orders?id=7" {
		t.Fatalf("Host header override did not select the header rule: %#v", result)
	}

	input.URL = "/nothing"
	input.Headers = nil
	result, err = simulateRoute(input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched || result.RouteIndex != -1 || result.Upstream != 0 || result.FinalURL != "http://default:8080/nothing" {
		t.Fatalf("unexpected fallback: %#v", result)
	}
}

func TestSimulateRouteRejectsBadInput(t *testing.T) {
	base := store.Config{Upstreams: []store.Upstream{{URL: "http://a"}}}
	tests := []struct {
		name  string
		input routeSimulationRequest
		want  string
	}{
		{"no upstream", routeSimulationRequest{URL: "/"}, "upstream"},
		{"empty url", routeSimulationRequest{Config: base}, "url is required"},
		{"bad scheme", routeSimulationRequest{Config: base, URL: "ftp://x/y"}, "http or https"},
		{"bad header", routeSimulationRequest{Config: base, URL: "/", Headers: []simulationHeader{{Name: "a b"}}}, "header name"},
		{"bad route", routeSimulationRequest{Config: store.Config{Upstreams: base.Upstreams, Routes: []routing.Route{{Path: "x"}}}, URL: "/"}, "must start with /"},
		{"bad upstream url", routeSimulationRequest{Config: store.Config{Upstreams: []store.Upstream{{URL: "not a url"}}}, URL: "/"}, "URL is invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := simulateRoute(tt.input); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestProxyRoutesRequestsToUpstreams(t *testing.T) {
	backend := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "%s %s %s", name, r.Host, r.URL.RequestURI())
		}))
	}
	web, api, canary := backend("web"), backend("api"), backend("canary")
	defer web.Close()
	defer api.Close()
	defer canary.Close()
	c := newTestControl(t)
	handler, err := c.proxyHandler(t.Context(), "routed", store.Config{
		Upstreams: []store.Upstream{{URL: web.URL}, {URL: api.URL + "/v1"}, {URL: canary.URL}},
		Routes: []routing.Route{
			{Path: "/api/*", StripPrefix: true, Upstream: 1},
			{Path: "/api/*", Headers: []routing.HeaderMatch{{Name: "X-Canary", Values: []string{"1"}}}, Upstream: 2},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		target, canary, want string
	}{
		{"/index.html", "", "web " + strings.TrimPrefix(web.URL, "http://") + " /index.html"},
		{"/api/users?page=2", "", "api " + strings.TrimPrefix(api.URL, "http://") + " /v1/users?page=2"},
		{"/api/users", "1", "canary " + strings.TrimPrefix(canary.URL, "http://") + " /api/users"},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, tt.target, nil)
		if tt.canary != "" {
			req.Header.Set("X-Canary", tt.canary)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusOK || response.Body.String() != tt.want {
			t.Fatalf("%s: got %d %q, want %q", tt.target, response.Code, response.Body.String(), tt.want)
		}
	}
}

func TestValidateRejectsInvalidRoutes(t *testing.T) {
	site := store.Site{ID: "s", Name: "s", Config: store.Config{ListenPort: 8080, Upstreams: []store.Upstream{{URL: "http://a"}}, Routes: []routing.Route{{Path: "/a", Upstream: 1}}}}
	if err := validate(site); err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("validate error = %v", err)
	}
	site.Config.Routes[0].Upstream = 0
	if err := validate(site); err != nil {
		t.Fatalf("valid route rejected: %v", err)
	}
}

func TestSimulateRouteAPI(t *testing.T) {
	handler := newTestControl(t).Handler()
	cookie := setupAdminForTest(t, handler)
	send := func(method, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "/api/routes/simulate", strings.NewReader(body))
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	body := `{"config":{"upstreams":[{"url":"http://a"},{"url":"http://b/base"}],"routes":[{"path":"/api/*","stripPrefix":true,"upstream":1}]},"url":"/api/x"}`
	response := send(http.MethodPost, body)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"finalUrl":"http://b/base/x"`) {
		t.Fatalf("simulate returned %d: %s", response.Code, response.Body.String())
	}
	if response := send(http.MethodPost, `{"config":{"upstreams":[]},"url":"/"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid simulation returned %d", response.Code)
	}
	if response := send(http.MethodGet, ""); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET returned %d", response.Code)
	}
}

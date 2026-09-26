package control

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"go.uber.org/zap"

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

func mustCompileRoutes(t *testing.T, routes []store.Route, upstreams int) siteRouter {
	t.Helper()
	router, err := compileRoutes(routes, upstreams)
	if err != nil {
		t.Fatalf("compileRoutes: %v", err)
	}
	return router
}

func TestCompileRoutesOrdersBySpecificity(t *testing.T) {
	routes := []store.Route{
		{Path: "/*", Upstream: 0},
		{Path: "/api/*", Upstream: 1},
		{Path: "/api/v1/*", Upstream: 2},
		{Path: "/api", Upstream: 3},
		{Headers: []store.HeaderMatch{{Name: "X-Canary"}}, Upstream: 0},
		{Path: "/api/*", Headers: []store.HeaderMatch{{Name: "X-Env", Values: []string{"beta"}}}, Upstream: 2},
		{Path: "/API*", Upstream: 1},
	}
	router := mustCompileRoutes(t, routes, 4)
	got := make([]int, 0, len(router.routes))
	for _, route := range router.routes {
		got = append(got, route.index)
	}
	// Longest path first; at equal length exact before prefix, then more header conditions, then configured order.
	// A header-only route behaves like "/*" and so beats the plain "/*" route through its header condition.
	want := []int{2, 5, 1, 3, 6, 4, 0}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("effective order = %v, want %v", got, want)
	}
}

func TestResolveRoute(t *testing.T) {
	routes := []store.Route{
		{Path: "/api/*", StripPrefix: true, Upstream: 1},
		{Path: "/static*", Upstream: 2},
		{Path: "/exact", Upstream: 3},
		{Path: "/api/*", Headers: []store.HeaderMatch{{Name: "X-Env", Values: []string{"canary", "beta*"}}}, Upstream: 4},
		{Path: "/h/*", Headers: []store.HeaderMatch{{Name: "User-Agent", Values: []string{"*bot"}}, {Name: "X-Debug", Absent: true}}, Upstream: 5},
		{Path: "/h/*", Headers: []store.HeaderMatch{{Name: "Authorization"}}, Upstream: 6},
		{Path: "/h/*", Headers: []store.HeaderMatch{{Name: "Accept", Values: []string{"*json*"}}}, Upstream: 7},
		{Headers: []store.HeaderMatch{{Name: "Host", Values: []string{"admin.*"}}}, Upstream: 8},
	}
	router := mustCompileRoutes(t, routes, 9)
	tests := []struct {
		name     string
		target   string
		headers  map[string][]string
		index    int
		upstream int
		strip    string
	}{
		{"prefix match", "/api/users", nil, 0, 1, "/api"},
		{"prefix match keeps trailing slash", "/api/", nil, 0, 1, "/api"},
		{"slash prefix does not match bare path", "/api", nil, -1, 0, ""},
		{"case insensitive", "/API/Users", nil, 0, 1, "/api"},
		{"cleaned double slashes", "//api//users", nil, 0, 1, "/api"},
		{"cleaned dot segments", "/static/../api/x", nil, 0, 1, "/api"},
		{"dot segments cannot escape a prefix", "/api/../exact", nil, 2, 3, ""},
		{"prefix without slash", "/static-files/app.js", nil, 1, 2, ""},
		{"exact", "/exact", nil, 2, 3, ""},
		{"exact rejects longer path", "/exact/more", nil, -1, 0, ""},
		{"fallback to first upstream", "/other", nil, -1, 0, ""},
		{"header exact value", "/api/x", map[string][]string{"X-Env": {"canary"}}, 3, 4, ""},
		{"header values are ORed", "/api/x", map[string][]string{"X-Env": {"beta-2"}}, 3, 4, ""},
		{"header value is case sensitive", "/api/x", map[string][]string{"X-Env": {"Canary"}}, 0, 1, "/api"},
		{"any of several header lines", "/api/x", map[string][]string{"X-Env": {"prod", "canary"}}, 3, 4, ""},
		{"header suffix and absent", "/h/a", map[string][]string{"User-Agent": {"googlebot"}}, 4, 5, ""},
		{"absent header present", "/h/a", map[string][]string{"User-Agent": {"googlebot"}, "X-Debug": {"1"}}, -1, 0, ""},
		{"header must exist", "/h/a", map[string][]string{"Authorization": {""}}, 5, 6, ""},
		{"header substring", "/h/a", map[string][]string{"Accept": {"application/json; q=1"}}, 6, 7, ""},
		{"host header", "http://admin.example.com/anything", nil, 7, 8, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			for name, values := range tt.headers {
				for _, value := range values {
					req.Header.Add(name, value)
				}
			}
			decision := router.resolve(req)
			if decision.index != tt.index || decision.upstream != tt.upstream || decision.stripPrefix != tt.strip {
				t.Fatalf("resolve(%s) = index %d upstream %d strip %q, want %d %d %q", tt.target, decision.index, decision.upstream, decision.stripPrefix, tt.index, tt.upstream, tt.strip)
			}
		})
	}
}

func TestCompileRoutesRejectsInvalidRules(t *testing.T) {
	tooMany := make([]store.Route, maxRoutes+1)
	for i := range tooMany {
		tooMany[i] = store.Route{Path: fmt.Sprintf("/r%d", i)}
	}
	tests := []struct {
		name   string
		routes []store.Route
		want   string
	}{
		{"upstream out of range", []store.Route{{Path: "/a", Upstream: 2}}, "upstream"},
		{"negative upstream", []store.Route{{Path: "/a", Upstream: -1}}, "upstream"},
		{"no condition", []store.Route{{Upstream: 0}}, "path or a header"},
		{"relative path", []store.Route{{Path: "api/*"}}, "must start with /"},
		{"inner wildcard", []store.Route{{Path: "/a/*/b"}}, "only at the end"},
		{"control character", []store.Route{{Path: "/a\nb"}}, "invalid character"},
		{"strip without path", []store.Route{{StripPrefix: true, Headers: []store.HeaderMatch{{Name: "X"}}}}, "stripPrefix requires a path"},
		{"absent with values", []store.Route{{Path: "/a", Headers: []store.HeaderMatch{{Name: "X", Absent: true, Values: []string{"1"}}}}}, "absent"},
		{"invalid header name", []store.Route{{Path: "/a", Headers: []store.HeaderMatch{{Name: "Bad Name"}}}}, "header name"},
		{"empty header value", []store.Route{{Path: "/a", Headers: []store.HeaderMatch{{Name: "X", Values: []string{""}}}}}, "empty value"},
		{"repeated header name", []store.Route{{Path: "/a", Headers: []store.HeaderMatch{{Name: "X"}, {Name: "x"}}}}, "more than once"},
		{"duplicate rule", []store.Route{{Path: "/A/*"}, {Path: "/a/*", Upstream: 1}}, "duplicates"},
		{"header-only duplicates catch-all", []store.Route{{Path: "/*", Headers: []store.HeaderMatch{{Name: "X"}}}, {Headers: []store.HeaderMatch{{Name: "x"}}}}, "duplicates"},
		{"too many", tooMany, "at most"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compileRoutes(tt.routes, 2)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("compileRoutes error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestApplyRouteRewritesPath(t *testing.T) {
	routes := []store.Route{
		{Path: "/api/*", StripPrefix: true, Upstream: 0},
		{Path: "/exact", StripPrefix: true, Upstream: 0},
		{Path: "/keep/*", Upstream: 0},
		{Path: "/files*", StripPrefix: true, Upstream: 0},
	}
	router := mustCompileRoutes(t, routes, 1)
	tests := []struct {
		name, upstream, target, want string
	}{
		{"strip onto root", "http://backend:8080", "/api/users?id=1", "http://backend:8080/users?id=1"},
		{"strip onto base path", "http://backend:8080/v2/", "/API/users", "http://backend:8080/v2/users"},
		{"strip to root", "http://backend:8080", "/api/", "http://backend:8080/"},
		{"exact strip becomes slash", "http://backend:8080/base", "/exact", "http://backend:8080/base/"},
		{"no strip keeps original path", "http://backend:8080/v1", "/keep//a", "http://backend:8080/v1/keep//a"},
		{"strip keeps escaped slash", "http://backend:8080", "/api/a%2Fb", "http://backend:8080/a%2Fb"},
		{"strip without slash boundary", "http://backend:8080", "/files-archive/x", "http://backend:8080/-archive/x"},
		{"queries merge", "http://backend:8080/?token=t", "/api/x?q=1", "http://backend:8080/x?token=t&q=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := url.Parse(tt.upstream)
			if err != nil {
				t.Fatal(err)
			}
			in := httptest.NewRequest(http.MethodGet, tt.target, nil)
			pr := &httputil.ProxyRequest{In: in, Out: in.Clone(in.Context())}
			applyRoute(pr, target, router.resolve(in))
			if got := pr.Out.URL.String(); got != tt.want {
				t.Fatalf("rewritten URL = %s, want %s", got, tt.want)
			}
			if pr.Out.Host != target.Host {
				t.Fatalf("outbound Host = %q, want %q", pr.Out.Host, target.Host)
			}
		})
	}
}

func TestExplainRouteReportsEachRule(t *testing.T) {
	routes := []store.Route{
		{Path: "/api/*", Upstream: 0},
		{Path: "/api/*", Headers: []store.HeaderMatch{{Name: "X-Env", Values: []string{"beta"}}}, Upstream: 1},
		{Path: "/other", Upstream: 1},
		{Path: "/*", Upstream: 1},
	}
	router := mustCompileRoutes(t, routes, 2)
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("X-Env", "prod")
	checks := router.explain(req)
	want := []string{"2:path:", "1:header:X-Env", "0:matched:", "3:skipped:"}
	got := make([]string, 0, len(checks))
	for _, check := range checks {
		got = append(got, fmt.Sprintf("%d:%s:%s", check.Index, check.Result, check.Header))
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("explain = %v, want %v", got, want)
	}
	if checks[1].Label != "/api/* [X-Env: beta]" || checks[1].Upstream != 1 {
		t.Fatalf("unexpected check: %#v", checks[1])
	}
}

func TestRouteLabel(t *testing.T) {
	tests := []struct {
		route store.Route
		want  string
	}{
		{store.Route{Path: "/api/*"}, "/api/*"},
		{store.Route{Headers: []store.HeaderMatch{{Name: "x-env", Values: []string{"a", "b*"}}}}, "* [X-Env: a|b*]"},
		{store.Route{Path: "/a", Headers: []store.HeaderMatch{{Name: "Authorization"}, {Name: "X-Debug", Absent: true}}}, "/a [Authorization] [!X-Debug]"},
	}
	for _, tt := range tests {
		if got := routeLabel(tt.route); got != tt.want {
			t.Fatalf("routeLabel = %q, want %q", got, tt.want)
		}
	}
}

func TestSimulateRouteShowsRewrite(t *testing.T) {
	input := routeSimulationRequest{
		Config: store.Config{
			Upstreams: []store.Upstream{{URL: "http://default:8080"}, {URL: "https://user:pass@api.internal/v2"}},
			Routes: []store.Route{
				{Path: "/api/*", StripPrefix: true, Upstream: 1},
				{Path: "/api/*", Headers: []store.HeaderMatch{{Name: "Host", Values: []string{"legacy.*"}}}, Upstream: 0},
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
	if len(result.Checks) != 2 || result.Checks[0].Result != routeHeaderFailed || result.Checks[1].Result != routeMatched {
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
		{"bad route", routeSimulationRequest{Config: store.Config{Upstreams: base.Upstreams, Routes: []store.Route{{Path: "x"}}}, URL: "/"}, "must start with /"},
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
		Routes: []store.Route{
			{Path: "/api/*", StripPrefix: true, Upstream: 1},
			{Path: "/api/*", Headers: []store.HeaderMatch{{Name: "X-Canary", Values: []string{"1"}}}, Upstream: 2},
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
	site := store.Site{ID: "s", Name: "s", Config: store.Config{ListenPort: 8080, Upstreams: []store.Upstream{{URL: "http://a"}}, Routes: []store.Route{{Path: "/a", Upstream: 1}}}}
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

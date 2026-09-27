package routing

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
)

func mustCompileRoutes(t *testing.T, routes []Route, upstreams int) Router {
	t.Helper()
	router, err := Compile(routes, upstreams)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return router
}

func TestCompileRoutesOrdersBySpecificity(t *testing.T) {
	routes := []Route{
		{Path: "/*", Upstream: 0},
		{Path: "/api/*", Upstream: 1},
		{Path: "/api/v1/*", Upstream: 2},
		{Path: "/api", Upstream: 3},
		{Headers: []HeaderMatch{{Name: "X-Canary"}}, Upstream: 0},
		{Path: "/api/*", Headers: []HeaderMatch{{Name: "X-Env", Values: []string{"beta"}}}, Upstream: 2},
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
	routes := []Route{
		{Path: "/api/*", StripPrefix: true, Upstream: 1},
		{Path: "/static*", Upstream: 2},
		{Path: "/exact", Upstream: 3},
		{Path: "/api/*", Headers: []HeaderMatch{{Name: "X-Env", Values: []string{"canary", "beta*"}}}, Upstream: 4},
		{Path: "/h/*", Headers: []HeaderMatch{{Name: "User-Agent", Values: []string{"*bot"}}, {Name: "X-Debug", Absent: true}}, Upstream: 5},
		{Path: "/h/*", Headers: []HeaderMatch{{Name: "Authorization"}}, Upstream: 6},
		{Path: "/h/*", Headers: []HeaderMatch{{Name: "Accept", Values: []string{"*json*"}}}, Upstream: 7},
		{Headers: []HeaderMatch{{Name: "Host", Values: []string{"admin.*"}}}, Upstream: 8},
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
			decision := router.Resolve(req)
			if decision.Index != tt.index || decision.Upstream != tt.upstream || decision.StripPrefix != tt.strip {
				t.Fatalf("resolve(%s) = index %d upstream %d strip %q, want %d %d %q", tt.target, decision.Index, decision.Upstream, decision.StripPrefix, tt.index, tt.upstream, tt.strip)
			}
		})
	}
}

func TestCompileRoutesRejectsInvalidRules(t *testing.T) {
	tooMany := make([]Route, MaxRoutes+1)
	for i := range tooMany {
		tooMany[i] = Route{Path: fmt.Sprintf("/r%d", i)}
	}
	tests := []struct {
		name   string
		routes []Route
		want   string
	}{
		{"upstream out of range", []Route{{Path: "/a", Upstream: 2}}, "upstream"},
		{"negative upstream", []Route{{Path: "/a", Upstream: -1}}, "upstream"},
		{"no condition", []Route{{Upstream: 0}}, "path or a header"},
		{"relative path", []Route{{Path: "api/*"}}, "must start with /"},
		{"inner wildcard", []Route{{Path: "/a/*/b"}}, "only at the end"},
		{"control character", []Route{{Path: "/a\nb"}}, "invalid character"},
		{"strip without path", []Route{{StripPrefix: true, Headers: []HeaderMatch{{Name: "X"}}}}, "stripPrefix requires a path"},
		{"absent with values", []Route{{Path: "/a", Headers: []HeaderMatch{{Name: "X", Absent: true, Values: []string{"1"}}}}}, "absent"},
		{"invalid header name", []Route{{Path: "/a", Headers: []HeaderMatch{{Name: "Bad Name"}}}}, "header name"},
		{"empty header value", []Route{{Path: "/a", Headers: []HeaderMatch{{Name: "X", Values: []string{""}}}}}, "empty value"},
		{"repeated header name", []Route{{Path: "/a", Headers: []HeaderMatch{{Name: "X"}, {Name: "x"}}}}, "more than once"},
		{"duplicate rule", []Route{{Path: "/A/*"}, {Path: "/a/*", Upstream: 1}}, "duplicates"},
		{"header-only duplicates catch-all", []Route{{Path: "/*", Headers: []HeaderMatch{{Name: "X"}}}, {Headers: []HeaderMatch{{Name: "x"}}}}, "duplicates"},
		{"too many", tooMany, "at most"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compile(tt.routes, 2)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Compile error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestApplyRouteRewritesPath(t *testing.T) {
	routes := []Route{
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
			Apply(pr, target, router.Resolve(in))
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
	routes := []Route{
		{Path: "/api/*", Upstream: 0},
		{Path: "/api/*", Headers: []HeaderMatch{{Name: "X-Env", Values: []string{"beta"}}}, Upstream: 1},
		{Path: "/other", Upstream: 1},
		{Path: "/*", Upstream: 1},
	}
	router := mustCompileRoutes(t, routes, 2)
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("X-Env", "prod")
	checks := router.Explain(req)
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
		route Route
		want  string
	}{
		{Route{Path: "/api/*"}, "/api/*"},
		{Route{Headers: []HeaderMatch{{Name: "x-env", Values: []string{"a", "b*"}}}}, "* [X-Env: a|b*]"},
		{Route{Path: "/a", Headers: []HeaderMatch{{Name: "Authorization"}, {Name: "X-Debug", Absent: true}}}, "/a [Authorization] [!X-Debug]"},
	}
	for _, tt := range tests {
		if got := Label(tt.route); got != tt.want {
			t.Fatalf("routeLabel = %q, want %q", got, tt.want)
		}
	}
}

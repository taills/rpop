package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rpop-project/rpop/internal/dataplane"
	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/snapshot"
	"github.com/rpop-project/rpop/internal/southbound"
	"github.com/rpop-project/rpop/internal/store"
)

func TestSelectSimulatedPath(t *testing.T) {
	tests := []struct {
		name         string
		cooling      []bool
		wantIndex    int
		wantFallback bool
	}{
		{"all healthy picks the first", []bool{false, false, false}, 0, false},
		{"first cooling skips to the second", []bool{true, false, false}, 1, false},
		{"only the last is healthy", []bool{true, true, false}, 2, false},
		{"all cooling falls back to the first", []bool{true, true, true}, 0, true},
		{"single healthy path", []bool{false}, 0, false},
		{"single cooling path falls back to itself", []bool{true}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index, reason := selectSimulatedPath(tt.cooling)
			if index != tt.wantIndex {
				t.Fatalf("index = %d, want %d", index, tt.wantIndex)
			}
			isFallback := reason == "全部路径冷却中，回退第一条冷却路径"
			if isFallback != tt.wantFallback {
				t.Fatalf("reason = %q, wantFallback = %v", reason, tt.wantFallback)
			}
		})
	}
}

// directPathsInput builds a routeSimulationRequest for one upstream with count all-direct candidate paths,
// placed on entryNode, so path selection can be exercised without any nodes or proxies in the via chain.
func directPathsInput(entryNode string, count int) routeSimulationRequest {
	paths := make([]store.UpstreamPath, count)
	return routeSimulationRequest{
		SiteID: "web",
		Config: store.Config{
			Nodes:     []string{entryNode},
			Upstreams: []store.Upstream{{URL: "https://backend.example", Paths: paths}},
		},
		URL: "/",
	}
}

func pathHealthStatus(index int, status string) dataplane.PathHealth {
	return dataplane.PathHealth{Index: index, Label: "direct", Status: status}
}

// TestAddPathSimulationSelectsFirstHealthyPath covers the three cooldown scenarios the live proxy's
// failoverTransport.order/RoundTrip can hit: an all-healthy set, a cooling first path, and every path cooling.
func TestAddPathSimulationSelectsFirstHealthyPath(t *testing.T) {
	c, s, _ := newPublishingControl(t)
	saveRelayNode(t, s, "node1", "10.0.0.1:7000", 1)
	c.nodes.connected("node1", 1)

	tests := []struct {
		name         string
		statuses     []string
		wantSelected int
		wantReason   string
	}{
		{"first healthy path wins", []string{"healthy", "healthy", "healthy"}, 0, "第一条未冷却的路径"},
		{"cooling first path falls through to the second", []string{"cooling", "healthy", "healthy"}, 1, "第一条未冷却的路径"},
		{"every path cooling falls back to the first", []string{"cooling", "cooling", "cooling"}, 0, "全部路径冷却中，回退第一条冷却路径"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			health := make([]dataplane.PathHealth, len(tt.statuses))
			for i, status := range tt.statuses {
				health[i] = pathHealthStatus(i, status)
			}
			c.nodes.report("node1", southbound.Status{Paths: []dataplane.UpstreamPathHealth{
				{SiteID: "web", Upstream: "https://backend.example", Paths: health},
			}})

			input := directPathsInput("node1", len(tt.statuses))
			result, err := simulateRoute(input)
			if err != nil {
				t.Fatal(err)
			}
			c.addPathSimulation(context.Background(), &result, input)

			if result.SelectedPath == nil || *result.SelectedPath != tt.wantSelected {
				t.Fatalf("selectedPath = %v, want %d", result.SelectedPath, tt.wantSelected)
			}
			if len(result.Paths) != len(tt.statuses) {
				t.Fatalf("paths = %#v, want %d entries", result.Paths, len(tt.statuses))
			}
			for i, status := range tt.statuses {
				if result.Paths[i].Status != status {
					t.Fatalf("paths[%d].status = %q, want %q", i, result.Paths[i].Status, status)
				}
				if result.Paths[i].Selected != (i == tt.wantSelected) {
					t.Fatalf("paths[%d].selected = %v, want %v", i, result.Paths[i].Selected, i == tt.wantSelected)
				}
			}
			if result.Paths[tt.wantSelected].Reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", result.Paths[tt.wantSelected].Reason, tt.wantReason)
			}
		})
	}
}

// TestAddPathSimulationRendersHopsWithLiveHealth builds an entry -> named proxy -> relay -> exit path and checks
// that every node hop reports whether it is online and, for hops reached over the overlay, the link health the
// node one hop back last reported (a down link and an offline exit both need to show up, not just healthy ones).
func TestAddPathSimulationRendersHopsWithLiveHealth(t *testing.T) {
	c, s, _ := newPublishingControl(t)
	saveRelayNode(t, s, "node1", "10.0.0.1:7000", 1)
	saveRelayNode(t, s, "node2", "10.0.0.2:7000", 1)
	saveRelayNode(t, s, "node3", "10.0.0.3:7001", 1)
	socksA := namedProxy{ID: "socks5-A", Type: "socks5h", Address: "10.1.0.1:1080", Username: "alice", Password: "secret-password"}
	if err := c.saveProxies(context.Background(), []namedProxy{socksA}); err != nil {
		t.Fatal(err)
	}
	c.nodes.connected("node1", 1)
	c.nodes.connected("node2", 1)
	// node3 is never marked connected: it must show up as offline.

	c.nodes.report("node1", southbound.Status{
		Links: []overlay.LinkStatus{{
			Peer: "node2", Address: "10.0.0.2:7000", Proxies: overlay.ProxyChainLabels([]snapshot.Proxy{socksA.snapshot()}),
			Status: "up", Connections: 2, Tunnels: 1,
		}},
		Paths: []dataplane.UpstreamPathHealth{{SiteID: "web", Upstream: "https://backend.example",
			Paths: []dataplane.PathHealth{{Index: 0, Label: "proxy:socks5-A > node2 > node3", Status: "healthy"}}}},
	})
	c.nodes.report("node2", southbound.Status{
		Links: []overlay.LinkStatus{{
			Peer: "node3", Address: "10.0.0.3:7001", Status: "down", DownUntil: "2099-01-01T00:00:00Z",
			LastError: "dial tcp 10.0.0.3:7001: connect: connection refused",
		}},
	})

	input := routeSimulationRequest{
		SiteID: "web",
		Config: store.Config{Nodes: []string{"node1"}, Upstreams: []store.Upstream{{
			URL:   "https://backend.example",
			Paths: []store.UpstreamPath{{Via: hops("proxy:socks5-A", "node2", "node3")}},
		}}},
		URL: "/",
	}
	result, err := simulateRoute(input)
	if err != nil {
		t.Fatal(err)
	}
	c.addPathSimulation(context.Background(), &result, input)

	if len(result.Paths) != 1 {
		t.Fatalf("paths = %#v, want 1 entry", result.Paths)
	}
	path := result.Paths[0]
	if path.Status != "healthy" || !path.Selected {
		t.Fatalf("path status/selected = %q/%v, want healthy/true", path.Status, path.Selected)
	}
	if len(path.Hops) != 4 {
		t.Fatalf("hops = %#v, want 4", path.Hops)
	}

	entry, proxyHop, relay, exit := path.Hops[0], path.Hops[1], path.Hops[2], path.Hops[3]
	if entry.Kind != "node" || entry.ID != "node1" || entry.Online == nil || !*entry.Online || entry.Link != nil {
		t.Fatalf("entry hop = %#v", entry)
	}
	if proxyHop.Kind != "proxy" || proxyHop.ID != "socks5-A" || proxyHop.Online != nil || proxyHop.Link != nil {
		t.Fatalf("proxy hop = %#v", proxyHop)
	}
	if relay.Kind != "node" || relay.ID != "node2" || relay.Online == nil || !*relay.Online {
		t.Fatalf("relay hop = %#v", relay)
	}
	if relay.Link == nil || relay.Link.Status != "up" {
		t.Fatalf("relay hop link = %#v, want status up (matched by peer+proxy chain)", relay.Link)
	}
	if exit.Kind != "node" || exit.ID != "node3" || exit.Online == nil || *exit.Online {
		t.Fatalf("exit hop = %#v, want online=false", exit)
	}
	if exit.Link == nil || exit.Link.Status != "down" || exit.Link.DownUntil == "" || exit.Link.LastError == "" {
		t.Fatalf("exit hop link = %#v, want a down status with downUntil/lastError", exit.Link)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-password") || strings.Contains(string(encoded), "alice") {
		t.Fatalf("simulated path response leaked proxy credentials: %s", encoded)
	}
}

// TestAddPathSimulationSkipsUpstreamsWithNoConfiguredPaths keeps the response backward compatible: an upstream
// with neither via nor paths gets no Paths/SelectedPath at all, not empty ones.
func TestAddPathSimulationSkipsUpstreamsWithNoConfiguredPaths(t *testing.T) {
	c, _, _ := newPublishingControl(t)
	input := routeSimulationRequest{Config: store.Config{Upstreams: []store.Upstream{{URL: "https://backend.example"}}}, URL: "/"}
	result, err := simulateRoute(input)
	if err != nil {
		t.Fatal(err)
	}
	c.addPathSimulation(context.Background(), &result, input)
	if result.Paths != nil || result.SelectedPath != nil {
		t.Fatalf("result = %#v, want no path fields for an upstream with no configured paths", result)
	}
}

// TestAddPathSimulationWithoutSiteIDReportsUnknownHealth covers a site that has never been saved (no id to
// correlate against live reports): every path still renders, just with unknown health, and selection still
// works by treating unknown like healthy (a fresh path is never cooling until its first failure).
func TestAddPathSimulationWithoutSiteIDReportsUnknownHealth(t *testing.T) {
	c, s, _ := newPublishingControl(t)
	saveRelayNode(t, s, "node1", "10.0.0.1:7000", 1)
	c.nodes.connected("node1", 1)

	input := directPathsInput("node1", 2)
	input.SiteID = ""
	result, err := simulateRoute(input)
	if err != nil {
		t.Fatal(err)
	}
	c.addPathSimulation(context.Background(), &result, input)

	if result.SelectedPath == nil || *result.SelectedPath != 0 {
		t.Fatalf("selectedPath = %v, want 0", result.SelectedPath)
	}
	for i, path := range result.Paths {
		if path.Status != "unknown" {
			t.Fatalf("paths[%d].status = %q, want unknown", i, path.Status)
		}
	}
}

// TestSimulateRouteAPIExtendsResponseForConfiguredPaths is the end-to-end check through the HTTP handler: the
// paths/selectedPath fields appear only when the matched upstream has candidate paths, and the JSON never
// carries a proxy credential.
func TestSimulateRouteAPIExtendsResponseForConfiguredPaths(t *testing.T) {
	handler := newTestControl(t).Handler()
	cookie := setupAdminForTest(t, handler)
	send := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/routes/simulate", strings.NewReader(body))
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	withoutPaths := send(`{"config":{"upstreams":[{"url":"http://a"}]},"url":"/"}`)
	if withoutPaths.Code != http.StatusOK {
		t.Fatalf("without paths: status %d: %s", withoutPaths.Code, withoutPaths.Body)
	}
	if strings.Contains(withoutPaths.Body.String(), `"paths"`) || strings.Contains(withoutPaths.Body.String(), `"selectedPath"`) {
		t.Fatalf("without paths: response leaked path fields: %s", withoutPaths.Body)
	}

	withPaths := send(`{"siteId":"web","config":{"upstreams":[{"url":"http://a","paths":[{"via":[]},{"via":[]}]}]},"url":"/"}`)
	if withPaths.Code != http.StatusOK {
		t.Fatalf("with paths: status %d: %s", withPaths.Code, withPaths.Body)
	}
	var decoded routeSimulationResult
	if err := json.Unmarshal(withPaths.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Paths) != 2 || decoded.SelectedPath == nil {
		t.Fatalf("with paths: decoded = %#v", decoded)
	}
}

package control

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/rpop-project/rpop/internal/dataplane"
	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/snapshot"
	"github.com/rpop-project/rpop/internal/southbound"
	"github.com/rpop-project/rpop/internal/store"
)

func TestNodesAPIManagesNodes(t *testing.T) {
	handler := newTestControl(t).Handler()
	cookie := setupAdminForTest(t, handler)
	call := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, response.Code, want, response.Body.String())
		}
		return response
	}
	token := func(response *httptest.ResponseRecorder) string {
		var body joinTokenResponse
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.JoinToken
	}

	for _, invalid := range []string{
		`{"id":"local","name":"x"}`, `{"id":"Edge","name":"x"}`, `{"id":"edge-1","name":" "}`,
		`{"id":"edge-1","name":"x","relayAddress":"nohost"}`, `{"id":"edge-1","name":"x","relayAddress":"h:0"}`,
	} {
		call(http.MethodPost, "/api/nodes", invalid, http.StatusBadRequest)
	}
	first := token(call(http.MethodPost, "/api/nodes", `{"id":"edge-1","name":"Edge 1","relayAddress":"203.0.113.5:7443"}`, http.StatusCreated))
	if !strings.HasPrefix(first, "rpop1.edge-1.") {
		t.Fatalf("join token = %q", first)
	}
	call(http.MethodPost, "/api/nodes", `{"id":"edge-1","name":"again"}`, http.StatusConflict)

	var nodes []nodeView
	if err := json.Unmarshal(call(http.MethodGet, "/api/nodes", "", http.StatusOK).Body.Bytes(), &nodes); err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || nodes[0].ID != LocalNodeID || !nodes[0].Embedded || nodes[1].ID != "edge-1" || nodes[1].Registered || nodes[1].Online {
		t.Fatalf("nodes = %#v", nodes)
	}
	if strings.Contains(call(http.MethodGet, "/api/nodes", "", http.StatusOK).Body.String(), "tokenHash") {
		t.Fatal("the node list exposes the join token hash")
	}

	call(http.MethodPut, "/api/nodes/edge-1", `{"name":"Edge One","relayAddress":"bad"}`, http.StatusBadRequest)
	call(http.MethodPut, "/api/nodes/edge-1", `{"name":"Edge One","relayAddress":"edge1.example.com:7443"}`, http.StatusOK)
	if second := token(call(http.MethodPost, "/api/nodes/edge-1/token", "", http.StatusOK)); second == first || second == "" {
		t.Fatal("reissuing did not produce a new join token")
	}
	call(http.MethodPut, "/api/nodes/missing", `{"name":"x"}`, http.StatusNotFound)

	call(http.MethodPost, "/api/sites", `{"id":"far","name":"far","config":{"nodes":["edge-9"],"listenPort":8080,"upstreams":[{"url":"http://a"}]}}`, http.StatusBadRequest)
	call(http.MethodPost, "/api/sites", `{"id":"edge","name":"edge","config":{"nodes":["edge-1"],"listenPort":8080,"upstreams":[{"url":"http://a"}]}}`, http.StatusCreated)
	call(http.MethodDelete, "/api/nodes/edge-1", "", http.StatusConflict)
	call(http.MethodDelete, "/api/sites/edge", "", http.StatusNoContent)
	call(http.MethodDelete, "/api/nodes/edge-1", "", http.StatusNoContent)
	call(http.MethodDelete, "/api/nodes/edge-1", "", http.StatusNotFound)
}

// TestNodeAPIGetSingleNode covers GET /api/nodes/{id}: the shape it returns must match the corresponding entry
// in GET /api/nodes (including the embedded node and health fields), and an unknown id must 404 like every
// other node endpoint does.
func TestNodeAPIGetSingleNode(t *testing.T) {
	c := newTestControl(t)
	handler := c.Handler()
	cookie := setupAdminForTest(t, handler)
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	registered := store.Node{ID: "edge-1", Name: "Edge 1", CertGeneration: 3, CreatedAt: "2024-01-01T00:00:00Z"}
	if err := c.store.SaveNode(t.Context(), registered); err != nil {
		t.Fatal(err)
	}
	c.nodes.report("edge-1", southbound.Status{Links: []overlay.LinkStatus{{Peer: "edge-2", Status: "up"}}})

	cases := []struct {
		name       string
		path       string
		embedded   bool
		wantStatus int
	}{
		{name: "embedded node", path: "/api/nodes/local", embedded: true, wantStatus: http.StatusOK},
		{name: "embedded node disabled", path: "/api/nodes/local", embedded: false, wantStatus: http.StatusNotFound},
		{name: "registered node", path: "/api/nodes/edge-1", embedded: true, wantStatus: http.StatusOK},
		{name: "unknown node", path: "/api/nodes/missing", embedded: true, wantStatus: http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c.SetEmbeddedNode(tc.embedded)
			t.Cleanup(func() { c.SetEmbeddedNode(true) })
			response := get(tc.path)
			if response.Code != tc.wantStatus {
				t.Fatalf("GET %s = %d, want %d: %s", tc.path, response.Code, tc.wantStatus, response.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			var got nodeView
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}

			var list []nodeView
			if err := json.Unmarshal(get("/api/nodes").Body.Bytes(), &list); err != nil {
				t.Fatal(err)
			}
			index := slices.IndexFunc(list, func(v nodeView) bool { return v.ID == got.ID })
			if index < 0 {
				t.Fatalf("node %q not present in list view", got.ID)
			}
			want := list[index]
			if got.CertGeneration != want.CertGeneration || got.Embedded != want.Embedded ||
				got.Online != want.Online || len(got.Links) != len(want.Links) {
				t.Fatalf("single node view = %#v, want %#v", got, want)
			}
		})
	}
}

func TestSiteMetricsAddUpNodes(t *testing.T) {
	c := newTestControl(t)
	c.nodes.report("edge-1", southbound.Status{Metrics: map[string]dataplane.MetricsSnapshot{"web": {
		RequestCount: 3, ErrorCount: 1, AverageResponseMillis: 10, P95ResponseMillis: 20, StatusCodes: map[int]uint64{200: 2, 502: 1},
	}}})
	c.nodes.report("edge-2", southbound.Status{Metrics: map[string]dataplane.MetricsSnapshot{"web": {
		RequestCount: 1, AverageResponseMillis: 30, P95ResponseMillis: 50, StatusCodes: map[int]uint64{200: 1},
	}}})
	view := c.siteMetrics(store.Site{ID: "web", Config: store.Config{Nodes: []string{"edge-1", "edge-2", "edge-3"}}})
	if view.RequestCount != 4 || view.ErrorCount != 1 || view.AverageResponseMillis != 15 || view.P95ResponseMillis != 50 || view.StatusCodes[200] != 3 {
		t.Fatalf("merged metrics = %#v", view.MetricsSnapshot)
	}
	if len(view.ByNode) != 3 || view.ByNode["edge-1"].RequestCount != 3 || view.ByNode["edge-3"].RequestCount != 0 {
		t.Fatalf("per-node metrics = %#v", view.ByNode)
	}
}

func TestNodeViewExposesReportedLinkAndPathHealth(t *testing.T) {
	c := newTestControl(t)
	c.nodes.report("edge-1", southbound.Status{
		Links: []overlay.LinkStatus{{Peer: "edge-2", Address: "10.0.0.2:7000", Status: "up", Connections: 1}},
		Paths: []dataplane.UpstreamPathHealth{{SiteID: "web", Upstream: "https://example.com", Paths: []dataplane.PathHealth{
			{Index: 0, Label: "direct", Status: "healthy"},
		}}},
	})
	view := c.nodeView(store.Node{ID: "edge-1", Name: "Edge 1"})
	if len(view.Links) != 1 || view.Links[0].Peer != "edge-2" || view.Links[0].Status != "up" {
		t.Fatalf("links = %#v", view.Links)
	}
	if len(view.Paths) != 1 || view.Paths[0].SiteID != "web" || len(view.Paths[0].Paths) != 1 || view.Paths[0].Paths[0].Label != "direct" {
		t.Fatalf("paths = %#v", view.Paths)
	}

	// A node that never reported carries no link/path health.
	unreported := c.nodeView(store.Node{ID: "edge-2", Name: "Edge 2"})
	if unreported.Links == nil || len(unreported.Links) != 0 || unreported.Paths != nil {
		t.Fatalf("unreported node view = %#v", unreported)
	}
}

// TestLocalNodeViewReportsEnginePathHealth covers the embedded node: its path health comes straight from the
// controller's own engine (see localNodeView), not from a status report.
func TestLocalNodeViewReportsEnginePathHealth(t *testing.T) {
	c := newTestControl(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	target := strings.TrimPrefix(upstream.URL, "http://")
	site := snapshot.Site{ID: "s", ListenAddress: "127.0.0.1", ListenPort: port,
		Upstreams: []snapshot.Upstream{{URL: upstream.URL, Paths: []snapshot.Path{{Label: "direct", Target: target}}}}}
	if errs := c.engine.Apply([]snapshot.Site{site}); len(errs) > 0 {
		t.Fatalf("apply failed: %v", errs)
	}
	t.Cleanup(c.engine.StopAll)

	view := c.localNodeView()
	if len(view.Paths) != 1 || view.Paths[0].SiteID != "s" || len(view.Paths[0].Paths) != 1 || view.Paths[0].Paths[0].Label != "direct" {
		t.Fatalf("local node paths = %#v", view.Paths)
	}
}

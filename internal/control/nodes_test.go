package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rpop-project/rpop/internal/dataplane"
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

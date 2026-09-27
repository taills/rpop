package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/rpop-project/rpop/internal/snapshot"
	"github.com/rpop-project/rpop/internal/store"
)

func hops(spec ...string) []store.Hop {
	via := make([]store.Hop, 0, len(spec))
	for _, hop := range spec {
		if id, ok := strings.CutPrefix(hop, "proxy:"); ok {
			via = append(via, store.Hop{Proxy: id})
		} else {
			via = append(via, store.Hop{Node: hop})
		}
	}
	return via
}

func TestValidateRejectsMalformedPaths(t *testing.T) {
	base := func(u store.Upstream) store.Site {
		u.URL = "https://example.com"
		return store.Site{ID: "s", Name: "s", Config: store.Config{Nodes: []string{"node1"}, ListenPort: 443, Upstreams: []store.Upstream{u}}}
	}
	for name, upstream := range map[string]store.Upstream{
		"via and paths":        {Via: hops("node2"), Paths: []store.UpstreamPath{{Via: hops("node3")}}},
		"proxyUrl and paths":   {ProxyURL: "socks5://10.0.0.1:1080", Via: hops("node2")},
		"empty hop":            {Via: []store.Hop{{}}},
		"hop with both":        {Via: []store.Hop{{Node: "node2", Proxy: "p"}}},
		"embedded node relays": {Via: hops("local")},
		"ingress relays":       {Via: hops("node2", "node1")},
		"node twice":           {Via: hops("node2", "proxy:p", "node2")},
		"invalid node":         {Via: hops("Node_2")},
		"invalid proxy":        {Via: hops("proxy:a b")},
		"repeated path":        {Paths: []store.UpstreamPath{{Via: hops("node2")}, {Via: hops("node3")}, {Via: hops("node2")}}},
		"too many paths":       {Paths: slices.Repeat([]store.UpstreamPath{{}}, maxUpstreamPaths+1)},
		"too many hops":        {Via: hops("proxy:a", "proxy:b", "proxy:c", "proxy:d", "proxy:e", "proxy:f", "proxy:g", "proxy:h", "proxy:i")},
	} {
		if err := validate(base(upstream)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, upstream := range map[string]store.Upstream{
		"single path":           {Via: hops("proxy:socks5-A", "node2", "node3", "proxy:socks5-B")},
		"fallbacks with direct": {Paths: []store.UpstreamPath{{Via: hops("node2", "node3")}, {Via: hops("node3")}, {}}},
		"proxies only":          {Via: hops("proxy:a", "proxy:b")},
		"proxyType direct":      {ProxyType: "direct", Via: hops("node2")},
	} {
		if err := validate(base(upstream)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestUpstreamDialTarget(t *testing.T) {
	for _, tc := range []struct{ url, dial, want string }{
		{"https://example.com", "", "example.com:443"},
		{"http://example.com/x", "", "example.com:80"},
		{"https://example.com:8443", "", "example.com:8443"},
		{"https://[2001:db8::1]", "", "[2001:db8::1]:443"},
		{"https://example.com", "10.0.0.9", "10.0.0.9:443"},
		{"https://example.com", "10.0.0.9:9443", "10.0.0.9:9443"},
		{"http://example.com", "2001:db8::2", "[2001:db8::2]:80"},
	} {
		got, err := upstreamDialTarget(store.Upstream{URL: tc.url, DialAddress: tc.dial})
		if err != nil || got != tc.want {
			t.Errorf("upstreamDialTarget(%q, %q) = %q, %v; want %q", tc.url, tc.dial, got, err, tc.want)
		}
	}
}

func saveRelayNode(t *testing.T, s *store.Store, id, relayAddress string, generation int64) {
	t.Helper()
	if err := s.SaveNode(context.Background(), store.Node{ID: id, Name: id, RelayAddress: relayAddress, CertGeneration: generation}); err != nil {
		t.Fatal(err)
	}
}

func pathLabels(paths []snapshot.Path) []string {
	labels := make([]string, 0, len(paths))
	for _, path := range paths {
		labels = append(labels, path.Label)
	}
	return labels
}

func relayRoute(t *testing.T, s snapshot.Snapshot, key string) snapshot.RelayRoute {
	t.Helper()
	for _, route := range s.Relay {
		if route.Key == key {
			return route
		}
	}
	t.Fatalf("node %s has no relay route %s: %#v", s.NodeID, key, s.Relay)
	return snapshot.RelayRoute{}
}

func TestPublishRendersPathsRelayRoutesAndPeers(t *testing.T) {
	c, s, _ := newPublishingControl(t)
	saveRelayNode(t, s, "node1", "", 1)
	saveRelayNode(t, s, "node2", "10.0.0.2:7000", 3)
	saveRelayNode(t, s, "node3", "10.0.0.3:7001", 1)
	saveRelayNode(t, s, "node4", "", 2)
	socksA := namedProxy{ID: "socks5-A", Type: "socks5h", Address: "10.1.0.1:1080", Username: "alice", Password: "secret"}
	socksB := namedProxy{ID: "socks5-B", Type: "socks5", Address: "10.1.0.2:1080"}
	if err := c.saveProxies(context.Background(), []namedProxy{socksA, socksB}); err != nil {
		t.Fatal(err)
	}
	site := store.Site{ID: "web", Name: "web", Config: store.Config{Nodes: []string{"node1", "node4"}, ListenPort: 443, Upstreams: []store.Upstream{{
		URL: "https://example.com",
		Paths: []store.UpstreamPath{
			{Via: hops("proxy:socks5-A", "node2", "node3", "proxy:socks5-B")},
			{Via: hops("node2", "node3")},
			{Via: hops("node3")},
			{},
		},
	}}}}
	if err := s.Save(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if err := c.start(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}

	snapshots := map[string]snapshot.Snapshot{}
	for _, id := range []string{"node1", "node2", "node3", "node4"} {
		snapshots[id], _ = c.published.Snapshot(id)
	}
	for _, ingress := range []string{"node1", "node4"} {
		paths := snapshots[ingress].Sites[0].Upstreams[0].Paths
		want := []string{"proxy:socks5-A > node2 > node3 > proxy:socks5-B", "node2 > node3", "node3", "direct"}
		if !slices.Equal(pathLabels(paths), want) {
			t.Fatalf("%s paths = %v, want %v", ingress, pathLabels(paths), want)
		}
		if paths[0].FirstNode != "node2" || !slices.Equal(paths[0].LinkProxies, []snapshot.Proxy{socksA.snapshot()}) || paths[0].Egress != nil || paths[0].Target != "example.com:443" {
			t.Fatalf("%s primary path = %#v", ingress, paths[0])
		}
		if paths[3].FirstNode != "" || paths[3].Key != "" || paths[3].Target != "example.com:443" {
			t.Fatalf("%s direct path = %#v", ingress, paths[3])
		}
		if got := snapshots[ingress].Peers; !slices.Equal(got, []snapshot.Peer{{ID: "node2", Address: "10.0.0.2:7000", Generation: 3}, {ID: "node3", Address: "10.0.0.3:7001", Generation: 1}}) {
			t.Fatalf("%s peers = %#v", ingress, got)
		}
		if snapshots[ingress].RelayListen != "" || len(snapshots[ingress].Relay) != 0 {
			t.Fatalf("ingress %s got a relay port", ingress)
		}
	}
	paths := snapshots["node1"].Sites[0].Upstreams[0].Paths
	if paths[0].Key == paths[1].Key || paths[1].Key == paths[2].Key {
		t.Fatal("different paths share a relay route key")
	}

	relay := snapshots["node2"]
	if relay.RelayListen != ":7000" || len(relay.Relay) != 2 || len(relay.Sites) != 0 {
		t.Fatalf("node2 relay = %q %#v", relay.RelayListen, relay.Relay)
	}
	for _, key := range []string{paths[0].Key, paths[1].Key} {
		route := relayRoute(t, relay, key)
		if !slices.Equal(route.From, []string{"node1", "node4"}) || route.Next != "node3" || route.Egress != nil {
			t.Fatalf("node2 route = %#v", route)
		}
	}

	exit := snapshots["node3"]
	if exit.RelayListen != ":7001" || len(exit.Relay) != 3 {
		t.Fatalf("node3 relay = %q %#v", exit.RelayListen, exit.Relay)
	}
	if route := relayRoute(t, exit, paths[0].Key); !slices.Equal(route.From, []string{"node2"}) || route.Next != "" ||
		!slices.Equal(route.Egress, []snapshot.Proxy{socksB.snapshot()}) || route.Target != "example.com:443" {
		t.Fatalf("node3 exit route with egress proxy = %#v", route)
	}
	if route := relayRoute(t, exit, paths[1].Key); !slices.Equal(route.From, []string{"node2"}) || route.Egress != nil {
		t.Fatalf("node3 exit route = %#v", route)
	}
	if route := relayRoute(t, exit, paths[2].Key); !slices.Equal(route.From, []string{"node1", "node4"}) {
		t.Fatalf("node3 route from the ingress nodes = %#v", route)
	}
	var peerIDs []string
	for _, peer := range exit.Peers {
		peerIDs = append(peerIDs, peer.ID)
		if peer.ID == "node4" && peer.Generation != 2 {
			t.Fatalf("node3 accepts node4 at generation %d, want 2", peer.Generation)
		}
	}
	if !slices.Equal(peerIDs, []string{"node1", "node2", "node4"}) {
		t.Fatalf("node3 peers = %v", peerIDs)
	}
}

func TestProxiesAPIHidesPasswordsAndProtectsReferencedProxies(t *testing.T) {
	c, s, _ := newPublishingControl(t)
	handler := c.Handler()
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
	for _, invalid := range []string{
		`{"id":"bad id","type":"socks5","address":"10.0.0.1:1080"}`,
		`{"id":"p","type":"ftp","address":"10.0.0.1:1080"}`,
		`{"id":"p","type":"socks5","address":"10.0.0.1"}`,
		`{"id":"p","type":"socks5","address":"10.0.0.1:0"}`,
	} {
		call(http.MethodPost, "/api/proxies", invalid, http.StatusBadRequest)
	}
	call(http.MethodPost, "/api/proxies", `{"id":"socks5-A","name":"A","type":"socks5h","address":"10.0.0.1:1080","username":"alice","password":"s3cret-value"}`, http.StatusCreated)
	call(http.MethodPost, "/api/proxies", `{"id":"socks5-A","type":"socks5","address":"10.0.0.1:1080"}`, http.StatusConflict)
	listing := call(http.MethodGet, "/api/proxies", "", http.StatusOK).Body.String()
	if strings.Contains(listing, "s3cret-value") || !strings.Contains(listing, `"hasPassword":true`) {
		t.Fatalf("proxy listing = %s", listing)
	}

	call(http.MethodPut, "/api/proxies/socks5-A", `{"name":"A","type":"socks5h","address":"10.0.0.2:1080","username":"alice"}`, http.StatusOK)
	if p, _ := c.proxyByID(context.Background(), "socks5-A"); p.Password != "s3cret-value" || p.Address != "10.0.0.2:1080" {
		t.Fatalf("an update without a password lost it: %#v", p)
	}
	call(http.MethodPut, "/api/proxies/socks5-A", `{"id":"other","type":"socks5h","address":"10.0.0.2:1080"}`, http.StatusBadRequest)
	call(http.MethodPut, "/api/proxies/missing", `{"type":"socks5h","address":"10.0.0.2:1080"}`, http.StatusNotFound)

	saveRelayNode(t, s, "node1", "", 1)
	saveRelayNode(t, s, "node2", "10.0.0.20:7000", 1)
	saveRelayNode(t, s, "node3", "", 1)
	site := func(via string) string {
		return `{"id":"web","name":"web","config":{"nodes":["node1"],"listenPort":8443,"upstreams":[{"url":"https://example.com","via":` + via + `}]}}`
	}
	call(http.MethodPost, "/api/sites", site(`[{"proxy":"missing"},{"node":"node2"}]`), http.StatusBadRequest)
	call(http.MethodPost, "/api/sites", site(`[{"node":"node3"}]`), http.StatusBadRequest)
	call(http.MethodPost, "/api/sites", site(`[{"node":"node9"}]`), http.StatusBadRequest)
	call(http.MethodPost, "/api/sites", site(`[{"proxy":"socks5-A"},{"node":"node2"}]`), http.StatusCreated)
	call(http.MethodPost, "/api/sites/web/start", "", http.StatusOK)

	call(http.MethodDelete, "/api/proxies/socks5-A", "", http.StatusConflict)
	call(http.MethodDelete, "/api/nodes/node2", "", http.StatusConflict)
	call(http.MethodPut, "/api/nodes/node2", `{"name":"node2"}`, http.StatusConflict)
	var views []proxyView
	if err := json.Unmarshal(call(http.MethodGet, "/api/proxies", "", http.StatusOK).Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || !slices.Equal(views[0].UsedBy, []string{"web"}) {
		t.Fatalf("proxy views = %#v", views)
	}

	// Nodes get proxy definitions inline, so changing a proxy renders the sites that use it again.
	call(http.MethodPut, "/api/proxies/socks5-A", `{"type":"socks5h","address":"10.0.0.3:1080","username":"alice"}`, http.StatusOK)
	published, _ := c.published.Snapshot("node1")
	if got := published.Sites[0].Upstreams[0].Paths[0].LinkProxies; len(got) != 1 || got[0].Address != "10.0.0.3:1080" || got[0].Password != "s3cret-value" {
		t.Fatalf("published link proxies after the update = %#v", got)
	}

	call(http.MethodDelete, "/api/sites/web", "", http.StatusNoContent)
	call(http.MethodDelete, "/api/proxies/socks5-A", "", http.StatusNoContent)
	call(http.MethodDelete, "/api/proxies/socks5-A", "", http.StatusNotFound)
}

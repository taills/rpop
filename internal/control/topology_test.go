package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/snapshot"
	"github.com/rpop-project/rpop/internal/southbound"
	"github.com/rpop-project/rpop/internal/store"
)

func findTopologyNode(t *testing.T, nodes []topologyNode, id string) topologyNode {
	t.Helper()
	for _, n := range nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("no node %q in topology: %#v", id, nodes)
	return topologyNode{}
}

func findTopologyLink(t *testing.T, links []topologyLink, from, to string) topologyLink {
	t.Helper()
	for _, l := range links {
		if l.From == from && l.To == to {
			return l
		}
	}
	t.Fatalf("no link %s -> %s in topology: %#v", from, to, links)
	return topologyLink{}
}

// TestTopologyAPIDerivesRolesLinksAndProxies builds the same ingress -> relay -> exit fixture as
// TestPublishRendersPathsRelayRoutesAndPeers and checks the topology view derived from it: node roles, the
// directed edges with their proxy chains, live health matched from a reported link, and the proxy list.
func TestTopologyAPIDerivesRolesLinksAndProxies(t *testing.T) {
	c, s, _ := newPublishingControl(t)
	saveRelayNode(t, s, "node1", "", 1)
	saveRelayNode(t, s, "node2", "10.0.0.2:7000", 3)
	saveRelayNode(t, s, "node3", "10.0.0.3:7001", 1)
	saveRelayNode(t, s, "node4", "", 2)
	socksA := namedProxy{ID: "socks5-A", Type: "socks5h", Address: "10.1.0.1:1080", Username: "alice", Password: "secret-password"}
	socksB := namedProxy{ID: "socks5-B", Type: "socks5", Address: "10.1.0.2:1080"}
	if err := c.saveProxies(context.Background(), []namedProxy{socksA, socksB}); err != nil {
		t.Fatal(err)
	}
	site := store.Site{ID: "web", Name: "web", Config: store.Config{Nodes: []string{"node1", "node4"}, ListenPort: 443, Upstreams: []store.Upstream{{
		URL: "https://example.com",
		Paths: []store.UpstreamPath{
			{Via: hops("proxy:socks5-A", "node2", "node3", "proxy:socks5-B")},
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

	// node1 reports its own outbound link health for the ingress -> node2 hop through socks5-A; node2's own
	// hop to node3 and node4's hops are left unreported to exercise the "unknown" fallback.
	c.nodes.report("node1", southbound.Status{Links: []overlay.LinkStatus{
		{Peer: "node2", Address: "10.0.0.2:7000", Proxies: overlay.ProxyChainLabels([]snapshot.Proxy{socksA.snapshot()}),
			Status: "up", Connections: 1, Tunnels: 2},
	}})

	view, err := c.buildTopology(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if roles := findTopologyNode(t, view.Nodes, "node1").Roles; !slices.Equal(roles, []string{"entry", "exit"}) {
		t.Fatalf("node1 roles = %v", roles)
	}
	if roles := findTopologyNode(t, view.Nodes, "node2").Roles; !slices.Equal(roles, []string{"relay"}) {
		t.Fatalf("node2 roles = %v", roles)
	}
	if roles := findTopologyNode(t, view.Nodes, "node3").Roles; !slices.Equal(roles, []string{"exit"}) {
		t.Fatalf("node3 roles = %v", roles)
	}
	if roles := findTopologyNode(t, view.Nodes, "node4").Roles; !slices.Equal(roles, []string{"entry", "exit"}) {
		t.Fatalf("node4 roles = %v", roles)
	}
	// The embedded node hosts nothing here, so it has no derived role, but it must still be listed.
	if local := findTopologyNode(t, view.Nodes, LocalNodeID); !local.Embedded || !local.Online || len(local.Roles) != 0 {
		t.Fatalf("local node = %#v", local)
	}

	want := []struct{ from, to string }{
		{"node1", "node2"}, {"node1", "node3"}, {"node4", "node2"}, {"node4", "node3"}, {"node2", "node3"},
	}
	if len(view.Links) != len(want) {
		t.Fatalf("links = %#v, want %d edges", view.Links, len(want))
	}
	for _, w := range want {
		findTopologyLink(t, view.Links, w.from, w.to)
	}

	reported := findTopologyLink(t, view.Links, "node1", "node2")
	if !slices.Equal(reported.Proxies, []string{"socks5h://10.1.0.1:1080"}) {
		t.Fatalf("node1->node2 proxies = %v", reported.Proxies)
	}
	if reported.Status != "up" || reported.Connections != 1 || reported.Tunnels != 2 {
		t.Fatalf("node1->node2 link = %#v", reported)
	}
	unreported := findTopologyLink(t, view.Links, "node2", "node3")
	if unreported.Status != "unknown" {
		t.Fatalf("node2->node3 status = %q, want unknown (node2 never reported)", unreported.Status)
	}
	direct := findTopologyLink(t, view.Links, "node1", "node3")
	if direct.Proxies != nil {
		t.Fatalf("node1->node3 proxies = %v, want none (no proxy hop)", direct.Proxies)
	}

	var proxyIDs []string
	for _, p := range view.Proxies {
		proxyIDs = append(proxyIDs, p.ID)
	}
	if !slices.Equal(proxyIDs, []string{"socks5-A", "socks5-B"}) {
		t.Fatalf("proxies = %v", proxyIDs)
	}

	if strings.Contains(fmt.Sprintf("%#v", view), "secret-password") {
		t.Fatal("topology response leaked a proxy credential")
	}
}

// TestTopologyAPIIncludesEmbeddedNodeAndOnlineStatus covers the simple, single-node case (no paths at all) and
// a registered node's online status.
func TestTopologyAPIIncludesEmbeddedNodeAndOnlineStatus(t *testing.T) {
	c, s, _ := newPublishingControl(t)
	site := store.Site{ID: "simple", Name: "simple", Config: store.Config{ListenPort: 443, Upstreams: []store.Upstream{{URL: "https://example.com"}}}}
	if err := s.Save(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if err := c.start(context.Background(), "simple"); err != nil {
		t.Fatal(err)
	}
	saveRelayNode(t, s, "edge-1", "", 0)
	c.nodes.connected("edge-1", 1)

	view, err := c.buildTopology(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	local := findTopologyNode(t, view.Nodes, LocalNodeID)
	if !local.Embedded || !local.Online || !slices.Equal(local.Roles, []string{"entry", "exit"}) {
		t.Fatalf("local node = %#v", local)
	}
	edge := findTopologyNode(t, view.Nodes, "edge-1")
	if edge.Embedded || !edge.Online || len(edge.Roles) != 0 {
		t.Fatalf("edge-1 node = %#v", edge)
	}
	// edge-1 hosts no site and relays nothing, so it has no derived role, but Roles must still marshal as an
	// empty array rather than null: the console's topology layout (web/src/topologyLayout.js) calls
	// roles.includes(...), which throws on null and white-screens the whole page (see the JS-side regression
	// test added alongside this one).
	if edge.Roles == nil {
		t.Fatal("edge-1 roles is a nil slice, will marshal as \"roles\":null")
	}
	encoded, err := json.Marshal(view.Nodes)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"roles":null`) {
		t.Fatalf("topology nodes JSON contains \"roles\":null: %s", encoded)
	}
	if len(view.Links) != 0 {
		t.Fatalf("links = %#v, want none for a site with no paths", view.Links)
	}
}

func TestTopologyAPIRequiresAuthentication(t *testing.T) {
	handler := newTestControl(t).Handler()
	setupAdminForTest(t, handler)
	request := httptest.NewRequest(http.MethodGet, "/api/topology", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /api/topology = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

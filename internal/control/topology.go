package control

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/snapshot"
	"github.com/rpop-project/rpop/internal/store"
)

// topologyView is the response of GET /api/topology: every known node with its derived role(s), the overlay
// links currently configured between them, and the named proxies that appear in some link's chain. The console
// lays this out entry -> relay -> exit (docs/architecture/control-data-plane.md §5, "阶段 6(控制台)设计").
type topologyView struct {
	Nodes   []topologyNode  `json:"nodes"`
	Links   []topologyLink  `json:"links"`
	Proxies []topologyProxy `json:"proxies"`
}

type topologyNode struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Embedded bool   `json:"embedded,omitempty"`
	Online   bool   `json:"online"`
	// Roles is a subset of "entry" (hosts a site), "relay" (forwards tunnels onward), and "exit" (dials the
	// upstream itself, directly or as the last hop of a tunnel); a node can hold more than one role at once.
	Roles []string `json:"roles"`
}

// topologyLink is one overlay link a node dials, derived from the paths and relay routes in the published
// snapshots. Status and counters are as reported by From's own outbound link (D15: a link is keyed by peer and
// proxy chain, and only the dialing side observes its own connections and failures) — see linkStatuses.
type topologyLink struct {
	From        string   `json:"from"`
	To          string   `json:"to"`
	Proxies     []string `json:"proxies,omitempty"`
	Status      string   `json:"status"`
	Connections int      `json:"connections"`
	Tunnels     int      `json:"tunnels"`
	Failures    int      `json:"failures,omitempty"`
	DownUntil   string   `json:"downUntil,omitempty"`
	LastError   string   `json:"lastError,omitempty"`
}

type topologyProxy struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func (c *Control) topologyAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
		return
	}
	view, err := c.buildTopology(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (c *Control) buildTopology(ctx context.Context) (topologyView, error) {
	nodes, err := c.store.ListNodes(ctx)
	if err != nil {
		return topologyView{}, err
	}
	snapshots := c.published.Snapshots()
	roles := deriveTopologyRoles(snapshots)

	view := topologyView{Nodes: []topologyNode{}, Links: []topologyLink{}, Proxies: []topologyProxy{}}
	if c.embeddedNode() {
		view.Nodes = append(view.Nodes, c.topologyNode(store.Node{ID: LocalNodeID, Name: "Embedded node"}, true, roles))
	}
	for _, node := range nodes {
		view.Nodes = append(view.Nodes, c.topologyNode(node, false, roles))
	}

	view.Links = c.topologyLinks(snapshots)

	proxies, err := c.loadedProxies(ctx)
	if err != nil {
		return topologyView{}, err
	}
	for _, p := range proxies {
		view.Proxies = append(view.Proxies, topologyProxy{ID: p.ID, Name: p.Name, Type: p.Type})
	}
	return view, nil
}

func (c *Control) topologyNode(node store.Node, embedded bool, roles map[string][]string) topologyNode {
	// A node that is registered but currently unused by any site or relay route has no entry in roles at all,
	// so roles[node.ID] is a nil slice; Roles has no `omitempty` (every node should report its role set, even
	// an empty one), so a nil slice would marshal as `"roles":null` instead of `[]`. The console's topology
	// layout (web/src/topologyLayout.js) expects an array it can call .includes on, so this always sends one.
	nodeRoles := roles[node.ID]
	if nodeRoles == nil {
		nodeRoles = []string{}
	}
	view := topologyNode{ID: node.ID, Name: node.Name, Embedded: embedded, Roles: nodeRoles}
	if embedded {
		view.Online = true
	} else if runtime, ok := c.nodes.snapshot(node.ID); ok {
		view.Online = runtime.streams > 0
	}
	return view
}

// topologyRoleOrder fixes the order roles appear in within one node's Roles list, matching the pipeline a
// request travels: entry -> relay -> exit.
var topologyRoleOrder = []string{"entry", "relay", "exit"}

// deriveTopologyRoles derives every node's role(s) from the currently published snapshots: a node that hosts a
// site is an entry; a relay route with a next hop makes the node a relay for that hop; a route with no next hop,
// or a site path with no first node (the ingress dials the upstream itself), makes the node an exit.
func deriveTopologyRoles(snapshots map[string]snapshot.Snapshot) map[string][]string {
	roleSets := make(map[string]map[string]bool, len(snapshots))
	set := func(nodeID, role string) {
		if roleSets[nodeID] == nil {
			roleSets[nodeID] = make(map[string]bool)
		}
		roleSets[nodeID][role] = true
	}
	for nodeID, snap := range snapshots {
		if len(snap.Sites) > 0 {
			set(nodeID, "entry")
		}
		for _, site := range snap.Sites {
			for _, upstream := range site.Upstreams {
				if len(upstream.Paths) == 0 {
					// No paths configured at all: the entry node dials the upstream directly, with no Path/
					// failover concept involved (see dataplane/proxy.go's newUpstreamTarget).
					set(nodeID, "exit")
					continue
				}
				for _, path := range upstream.Paths {
					if path.FirstNode == "" {
						set(nodeID, "exit")
					}
				}
			}
		}
		for _, route := range snap.Relay {
			if route.Next == "" {
				set(nodeID, "exit")
			} else {
				set(nodeID, "relay")
			}
		}
	}
	roles := make(map[string][]string, len(roleSets))
	for nodeID, set := range roleSets {
		var ordered []string
		for _, role := range topologyRoleOrder {
			if set[role] {
				ordered = append(ordered, role)
			}
		}
		roles[nodeID] = ordered
	}
	return roles
}

// topologyLinks derives every directed overlay link from the published snapshots: a site's path to its first
// node (with the path's own leading proxy chain), and every relay route's hop to its next node (with that
// route's link proxy chain). Both are already resolved per-node by renderOverlay/resolvePaths, so this only
// reads them back and attaches live health.
func (c *Control) topologyLinks(snapshots map[string]snapshot.Snapshot) []topologyLink {
	type edgeKey struct{ from, to, chain string }
	links := make(map[edgeKey]topologyLink)
	var order []edgeKey
	add := func(from, to string, proxies []snapshot.Proxy) {
		labels := overlay.ProxyChainLabels(proxies)
		key := edgeKey{from, to, strings.Join(labels, "\x00")}
		if _, exists := links[key]; exists {
			return
		}
		link := topologyLink{From: from, To: to, Proxies: labels, Status: "unknown"}
		if status, ok := matchLinkStatus(c.linkStatuses(from), to, labels); ok {
			link.Status, link.Connections, link.Tunnels, link.Failures = status.Status, status.Connections, status.Tunnels, status.Failures
			link.DownUntil, link.LastError = status.DownUntil, status.LastError
		}
		links[key] = link
		order = append(order, key)
	}
	for nodeID, snap := range snapshots {
		for _, site := range snap.Sites {
			for _, upstream := range site.Upstreams {
				for _, path := range upstream.Paths {
					if path.FirstNode != "" {
						add(nodeID, path.FirstNode, path.LinkProxies)
					}
				}
			}
		}
		for _, route := range snap.Relay {
			if route.Next != "" {
				add(nodeID, route.Next, route.LinkProxies)
			}
		}
	}
	result := make([]topologyLink, 0, len(order))
	for _, key := range order {
		result = append(result, links[key])
	}
	slices.SortFunc(result, func(a, b topologyLink) int {
		if c := strings.Compare(a.From, b.From); c != 0 {
			return c
		}
		if c := strings.Compare(a.To, b.To); c != 0 {
			return c
		}
		return strings.Compare(strings.Join(a.Proxies, "\x00"), strings.Join(b.Proxies, "\x00"))
	})
	return result
}

// linkStatuses returns the link health nodeID has most recently reported about itself: the embedded node's
// overlay is read live (it never reports its own status southbound), and every other node's is whatever its
// last status report carried (already sanitized by nodeRegistry.report).
func (c *Control) linkStatuses(nodeID string) []overlay.LinkStatus {
	if nodeID == LocalNodeID {
		c.opMu.Lock()
		o := c.overlay
		c.opMu.Unlock()
		if o == nil {
			return nil
		}
		return o.Links()
	}
	runtime, ok := c.nodes.snapshot(nodeID)
	if !ok {
		return nil
	}
	return runtime.status.Links
}

// matchLinkStatus finds the status of the link to peer whose proxy chain equals proxies; if none matches
// exactly (the node hasn't reported yet, or is reporting under a slightly different rendering), it falls back
// to the first status for that peer, on any chain, rather than reporting nothing.
func matchLinkStatus(statuses []overlay.LinkStatus, peer string, proxies []string) (overlay.LinkStatus, bool) {
	var fallback overlay.LinkStatus
	haveFallback := false
	for _, status := range statuses {
		if status.Peer != peer {
			continue
		}
		if slices.Equal(status.Proxies, proxies) {
			return status, true
		}
		if !haveFallback {
			fallback, haveFallback = status, true
		}
	}
	return fallback, haveFallback
}

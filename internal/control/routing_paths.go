package control

import (
	"context"

	"github.com/rpop-project/rpop/internal/dataplane"
	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/snapshot"
	"github.com/rpop-project/rpop/internal/store"
)

// simulatedHop is one step of a simulated path: the entry node, a relay node, or a named proxy (contract in
// docs/architecture/control-data-plane.md §5, "API 契约(6.5 定形)"; Online and Link extend it with the health
// detail the simulator needs that the minimal contract left out, see the stage 6 step 5 implementation note).
type simulatedHop struct {
	Kind string `json:"kind"` // "node" | "proxy"
	ID   string `json:"id"`
	// Online reports whether the node currently has an open connection to the controller; set only for
	// kind=="node" (the embedded node is always online while the controller process runs).
	Online *bool `json:"online,omitempty"`
	// Link is the health of the overlay tunnel arriving at this node hop, as most recently reported by the node
	// one hop back (D15/D19); nil for the entry hop (nothing arrives at it over the overlay) and for proxy hops.
	Link *simulatedLink `json:"link,omitempty"`
}

// simulatedLink mirrors overlay.LinkStatus for one hop-to-hop segment, without the peer/proxy fields the caller
// already knows from the surrounding simulatedHop pair.
type simulatedLink struct {
	Status    string `json:"status"` // up | dialing | down | unknown
	DownUntil string `json:"downUntil,omitempty"`
	LastError string `json:"lastError,omitempty"`
}

// simulatedPath is one candidate path of the matched upstream, rendered the same way the live proxy would try
// it (internal/dataplane/paths.go's failoverTransport.order).
type simulatedPath struct {
	Index int            `json:"index"`
	Label string         `json:"label"`
	Hops  []simulatedHop `json:"hops"`
	// Status is this path's own cooldown state as last reported by the entry node (D18/D19); "unknown" when
	// there is nothing to correlate it with (no SiteID given, the site has never run on its entry node, or the
	// config under test was edited since the last report).
	Status   string `json:"status"` // healthy | cooling | unknown
	Until    string `json:"until,omitempty"`
	Selected bool   `json:"selected"`
	Reason   string `json:"reason,omitempty"`
}

// addPathSimulation extends result with the matched upstream's candidate paths, when it has any configured; it
// leaves result untouched otherwise, keeping the response shape backward compatible. Best effort throughout:
// a proxy or node the tested config references but that no longer exists just renders with unknown health
// rather than failing the whole simulation, since simulation is meant to help debug a config that may not be
// saved (or valid) yet.
func (c *Control) addPathSimulation(ctx context.Context, result *routeSimulationResult, input routeSimulationRequest) {
	upstream := input.Config.Upstreams[result.Upstream]
	configured := upstreamPaths(upstream)
	if len(configured) == 0 {
		return
	}
	proxies, _ := c.loadedProxies(ctx)
	proxyByID := make(map[string]namedProxy, len(proxies))
	for _, p := range proxies {
		proxyByID[p.ID] = p
	}
	entryNode := siteNodes(input.Config)[0]
	live := c.liveUpstreamPathHealth(entryNode, input.SiteID, result.UpstreamURL)

	paths := make([]simulatedPath, len(configured))
	cooling := make([]bool, len(configured))
	for i, candidate := range configured {
		sp := simulatedPath{Index: i, Label: pathLabel(candidate.Via), Hops: c.simulatedHops(entryNode, candidate.Via, proxyByID), Status: "unknown"}
		if i < len(live) {
			sp.Status, sp.Until = live[i].Status, live[i].Until
		}
		cooling[i] = sp.Status == "cooling"
		paths[i] = sp
	}
	selected, reason := selectSimulatedPath(cooling)
	paths[selected].Selected, paths[selected].Reason = true, reason
	result.Paths, result.SelectedPath = paths, &selected
}

// selectSimulatedPath mirrors failoverTransport.order/RoundTrip (internal/dataplane/paths.go): the first path
// that is not cooling down wins; when every path is cooling, the first one in priority order is retried anyway
// (order() appends cooling paths in their original order, so RoundTrip always attempts index 0 of that group
// first). A path with unknown health (no live report) is treated like healthy for this choice, matching a
// freshly started pathTransport, which is never cooling until its first failure.
func selectSimulatedPath(cooling []bool) (int, string) {
	for i, isCooling := range cooling {
		if !isCooling {
			return i, "第一条未冷却的路径"
		}
	}
	return 0, "全部路径冷却中，回退第一条冷却路径"
}

// simulatedHops walks one candidate path's hop-notation Via and renders the full hop chain: the entry node,
// then every node/proxy the path passes through. Proxy IDs are emitted as-is (no credential resolution is
// needed for the display, and namedProxy.snapshot() is only used, via overlay.ProxyChainLabels, to build the
// label overlay.LinkStatus.Proxies was reported under, never serialized itself, so a chain's credentials never
// reach the response).
func (c *Control) simulatedHops(entryNode string, via []store.Hop, proxies map[string]namedProxy) []simulatedHop {
	online := c.nodeOnline(entryNode)
	hops := []simulatedHop{{Kind: "node", ID: entryNode, Online: &online}}
	prev := entryNode
	var pending []snapshot.Proxy
	for _, hop := range via {
		if hop.Proxy != "" {
			hops = append(hops, simulatedHop{Kind: "proxy", ID: hop.Proxy})
			if p, ok := proxies[hop.Proxy]; ok {
				pending = append(pending, p.snapshot())
			}
			continue
		}
		nodeOnline := c.nodeOnline(hop.Node)
		hops = append(hops, simulatedHop{Kind: "node", ID: hop.Node, Online: &nodeOnline, Link: c.hopLink(prev, hop.Node, pending)})
		prev, pending = hop.Node, nil
	}
	return hops
}

// hopLink reports the health of the overlay link from to as most recently reported by from (see
// Control.linkStatuses/matchLinkStatus in topology.go, reused as-is).
func (c *Control) hopLink(from, to string, proxies []snapshot.Proxy) *simulatedLink {
	status, ok := matchLinkStatus(c.linkStatuses(from), to, overlay.ProxyChainLabels(proxies))
	if !ok {
		return &simulatedLink{Status: "unknown"}
	}
	return &simulatedLink{Status: status.Status, DownUntil: status.DownUntil, LastError: status.LastError}
}

// nodeOnline reports whether a node currently has an open connection to the controller; the embedded node is
// online whenever the controller runs one, matching the convention topologyNode/localNodeView already use.
func (c *Control) nodeOnline(nodeID string) bool {
	if nodeID == LocalNodeID {
		return c.embeddedNode()
	}
	runtime, ok := c.nodes.snapshot(nodeID)
	return ok && runtime.streams > 0
}

// liveUpstreamPathHealth finds the candidate-path health entryNode last reported for siteID's upstream
// (matched by its redacted URL label, the same string newUpstreamTarget uses); nil when siteID is empty, the
// node has never reported, or it reports nothing for this site/upstream pair.
func (c *Control) liveUpstreamPathHealth(entryNode, siteID, upstreamLabel string) []dataplane.PathHealth {
	if siteID == "" {
		return nil
	}
	var groups []dataplane.UpstreamPathHealth
	if entryNode == LocalNodeID {
		groups = c.engine.PathHealth()
	} else if runtime, ok := c.nodes.snapshot(entryNode); ok {
		groups = runtime.status.Paths
	}
	for _, g := range groups {
		if g.SiteID == siteID && g.Upstream == upstreamLabel {
			return g.Paths
		}
	}
	return nil
}

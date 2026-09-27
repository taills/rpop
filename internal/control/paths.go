package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/rpop-project/rpop/internal/snapshot"
	"github.com/rpop-project/rpop/internal/store"
)

const (
	maxUpstreamPaths = 8
	maxPathHops      = 8
)

// upstreamPaths returns an upstream's candidate paths in priority order; via is shorthand for a single path.
func upstreamPaths(u store.Upstream) []store.UpstreamPath {
	if len(u.Via) > 0 {
		return []store.UpstreamPath{{Via: u.Via}}
	}
	return u.Paths
}

// pathLabel names a path in hop notation, e.g. "proxy:socks5-A > node2 > node3".
func pathLabel(via []store.Hop) string {
	if len(via) == 0 {
		return "direct"
	}
	parts := make([]string, 0, len(via))
	for _, hop := range via {
		if hop.Proxy != "" {
			parts = append(parts, "proxy:"+hop.Proxy)
		} else {
			parts = append(parts, hop.Node)
		}
	}
	return strings.Join(parts, " > ")
}

// validatePaths checks the shape of an upstream's paths. Whether their nodes and proxies exist is checked by
// validatePathReferences.
func validatePaths(cfg store.Config, index int, u store.Upstream) error {
	if len(u.Via) > 0 && len(u.Paths) > 0 {
		return fmt.Errorf("upstreams[%d] sets both via and paths; via is shorthand for a single path", index)
	}
	paths := upstreamPaths(u)
	if len(paths) == 0 {
		return nil
	}
	if u.ProxyURL != "" || (u.ProxyType != "" && u.ProxyType != "direct") {
		return fmt.Errorf("upstreams[%d] cannot combine proxyUrl with paths; register the proxy and add it to the path", index)
	}
	if len(paths) > maxUpstreamPaths {
		return fmt.Errorf("upstreams[%d] can have at most %d paths", index, maxUpstreamPaths)
	}
	ingress := siteNodes(cfg)
	seen := make(map[string]int, len(paths))
	for p, path := range paths {
		field := fmt.Sprintf("upstreams[%d].paths[%d]", index, p)
		if len(path.Via) > maxPathHops {
			return fmt.Errorf("%s can have at most %d hops", field, maxPathHops)
		}
		visited := make(map[string]bool, len(path.Via))
		for h, hop := range path.Via {
			switch {
			case (hop.Node == "") == (hop.Proxy == ""):
				return fmt.Errorf("%s.via[%d] must name exactly one node or proxy", field, h)
			case hop.Proxy != "":
				if !proxyIDPattern.MatchString(hop.Proxy) {
					return fmt.Errorf("%s.via[%d] names an invalid proxy id %q", field, h, hop.Proxy)
				}
			case hop.Node == LocalNodeID:
				return fmt.Errorf("%s.via[%d]: the embedded node cannot relay; only registered nodes can", field, h)
			case !nodeIDPattern.MatchString(hop.Node):
				return fmt.Errorf("%s.via[%d] names an invalid node id %q", field, h, hop.Node)
			case slices.Contains(ingress, hop.Node):
				return fmt.Errorf("%s.via[%d]: node %q serves the site, so a path cannot pass through it", field, h, hop.Node)
			case visited[hop.Node]:
				return fmt.Errorf("%s passes through node %q more than once", field, hop.Node)
			}
			if hop.Node != "" {
				visited[hop.Node] = true
			}
		}
		label := pathLabel(path.Via)
		if previous, repeated := seen[label]; repeated {
			return fmt.Errorf("%s repeats paths[%d] (%s)", field, previous, label)
		}
		seen[label] = p
	}
	return nil
}

// validatePathReferences checks that every node on a site's paths exists and can be reached by other nodes, and
// that every proxy is registered.
func (c *Control) validatePathReferences(ctx context.Context, site store.Site) error {
	for index, u := range site.Config.Upstreams {
		for p, path := range upstreamPaths(u) {
			for _, hop := range path.Via {
				field := fmt.Sprintf("upstreams[%d].paths[%d]", index, p)
				if hop.Proxy != "" {
					if _, err := c.proxyByID(ctx, hop.Proxy); err != nil {
						return fmt.Errorf("%s: %w", field, err)
					}
					continue
				}
				node, err := c.store.GetNode(ctx, hop.Node)
				if errors.Is(err, store.ErrNodeNotFound) {
					return fmt.Errorf("%s: node %q does not exist", field, hop.Node)
				}
				if err != nil {
					return err
				}
				if node.RelayAddress == "" {
					return fmt.Errorf("%s: node %q has no relay address, so other nodes cannot reach it", field, hop.Node)
				}
			}
		}
	}
	return nil
}

// plannedRoute is the relay route one node on a path needs; prev is the node the tunnel comes from, empty when
// it comes from the ingress, which is known only once the site is placed.
type plannedRoute struct {
	node, prev string
	route      snapshot.RelayRoute
}

// resolvePaths turns an upstream's paths into what the ingress dials and the routes every relay on them needs.
// Proxies before the first node carry the ingress's link to it, proxies between two nodes carry their link,
// and proxies after the last node are the exit's egress.
func (c *Control) resolvePaths(ctx context.Context, u store.Upstream) ([]snapshot.Path, []plannedRoute, error) {
	paths := upstreamPaths(u)
	if len(paths) == 0 {
		return nil, nil, nil
	}
	target, err := upstreamDialTarget(u)
	if err != nil {
		return nil, nil, err
	}
	type stop struct {
		node    string
		proxies []snapshot.Proxy
	}
	var resolved []snapshot.Path
	var routes []plannedRoute
	for _, path := range paths {
		var leading []snapshot.Proxy
		var stops []stop
		firstNode := -1
		for index, hop := range path.Via {
			if hop.Node != "" {
				if firstNode < 0 {
					firstNode = index
				}
				stops = append(stops, stop{node: hop.Node})
				continue
			}
			p, err := c.proxyByID(ctx, hop.Proxy)
			if err != nil {
				return nil, nil, err
			}
			if len(stops) == 0 {
				leading = append(leading, p.snapshot())
			} else {
				stops[len(stops)-1].proxies = append(stops[len(stops)-1].proxies, p.snapshot())
			}
		}
		candidate := snapshot.Path{Label: pathLabel(path.Via), Target: target}
		if len(stops) == 0 {
			candidate.Egress = leading
			resolved = append(resolved, candidate)
			continue
		}
		candidate.Key = routeKey(path.Via[firstNode:], target)
		candidate.FirstNode, candidate.LinkProxies = stops[0].node, leading
		prev := ""
		for index, s := range stops {
			route := snapshot.RelayRoute{Key: candidate.Key, Target: target}
			if index+1 < len(stops) {
				route.Next, route.LinkProxies = stops[index+1].node, s.proxies
			} else {
				route.Egress = s.proxies
			}
			routes = append(routes, plannedRoute{node: s.node, prev: prev, route: route})
			prev = s.node
		}
		resolved = append(resolved, candidate)
	}
	return resolved, routes, nil
}

// routeKey identifies the part of a path the relays carry. Paths from different ingress nodes, sites, or
// upstreams that share it share one relay route, whose From lists every node allowed to use it.
func routeKey(fromFirstNode []store.Hop, target string) string {
	data, _ := json.Marshal(struct {
		Hops   []store.Hop
		Target string
	}{fromFirstNode, target})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

// upstreamDialTarget is the host:port the exit of a path connects to: the dial address override if set, else
// the upstream URL's host with the scheme's default port.
func upstreamDialTarget(u store.Upstream) (string, error) {
	parsed, err := url.Parse(u.URL)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("invalid upstream URL")
	}
	port := "80"
	if parsed.Scheme == "https" {
		port = "443"
	}
	if u.DialAddress != "" {
		if _, _, err := net.SplitHostPort(u.DialAddress); err == nil {
			return u.DialAddress, nil
		}
		return net.JoinHostPort(strings.Trim(u.DialAddress, "[]"), port), nil
	}
	if parsed.Port() != "" {
		return parsed.Host, nil
	}
	return net.JoinHostPort(parsed.Hostname(), port), nil
}

// renderOverlay adds to every snapshot the relay routes its node forwards, the peers it dials or accepts
// tunnels from, and the address its relay port binds. A route is rendered once per key; each ingress node of
// a site that uses it, or the relay before it, is added to its From list.
func renderOverlay(snapshots map[string]snapshot.Snapshot, entries map[string]publishedSite, nodes map[string]store.Node) {
	routes := make(map[string]map[string]*snapshot.RelayRoute)
	peers := make(map[string]map[string]bool)
	addPeer := func(node, peer string) {
		if peers[node] == nil {
			peers[node] = make(map[string]bool)
		}
		peers[node][peer] = true
	}
	for _, entry := range entries {
		for _, ingress := range entry.nodes {
			if _, ok := snapshots[ingress]; !ok {
				continue
			}
			for _, upstream := range entry.spec.Upstreams {
				for _, path := range upstream.Paths {
					if path.FirstNode != "" {
						addPeer(ingress, path.FirstNode)
					}
				}
			}
			for _, planned := range entry.routes {
				from := planned.prev
				if from == "" {
					from = ingress
				}
				if routes[planned.node] == nil {
					routes[planned.node] = make(map[string]*snapshot.RelayRoute)
				}
				route := routes[planned.node][planned.route.Key]
				if route == nil {
					copied := planned.route
					copied.From = nil
					route = &copied
					routes[planned.node][planned.route.Key] = route
				}
				if !slices.Contains(route.From, from) {
					route.From = append(route.From, from)
				}
				addPeer(planned.node, from)
				if route.Next != "" {
					addPeer(planned.node, route.Next)
				}
			}
		}
	}
	for nodeID, s := range snapshots {
		keys := make([]string, 0, len(routes[nodeID]))
		for key := range routes[nodeID] {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			route := *routes[nodeID][key]
			slices.Sort(route.From)
			s.Relay = append(s.Relay, route)
		}
		ids := make([]string, 0, len(peers[nodeID]))
		for id := range peers[nodeID] {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			s.Peers = append(s.Peers, snapshot.Peer{ID: id, Address: nodes[id].RelayAddress, Generation: nodes[id].CertGeneration})
		}
		if len(s.Relay) > 0 {
			s.RelayListen = relayListenAddress(nodes[nodeID].RelayAddress)
		}
		snapshots[nodeID] = s
	}
}

// relayListenAddress binds the relay port on every interface at the port other nodes dial; a node behind port
// forwarding overrides it locally.
func relayListenAddress(relayAddress string) string {
	_, port, err := net.SplitHostPort(relayAddress)
	if err != nil {
		return ""
	}
	return net.JoinHostPort("", port)
}

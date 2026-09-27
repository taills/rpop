package overlay

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/snapshot"
)

// Overlay is a node's side of the overlay network: its links to peers, the tunnels it opens on them, and its
// relay port.
type Overlay struct {
	identity *pki.Identity
	log      *zap.Logger
	peers    atomic.Pointer[map[string]snapshot.Peer]
	routes   atomic.Pointer[map[string]snapshot.RelayRoute]

	mu    sync.Mutex
	links map[string]*link
	relay *relayServer
}

// New creates the overlay of the node identity names.
func New(identity *pki.Identity, log *zap.Logger) *Overlay {
	o := &Overlay{identity: identity, log: log, links: make(map[string]*link)}
	o.peers.Store(&map[string]snapshot.Peer{})
	o.routes.Store(&map[string]snapshot.RelayRoute{})
	return o
}

// Apply installs the peers, relay routes, and links a snapshot needs. Links that are no longer needed stop
// taking tunnels and close once their open tunnels end. The error reports a relay port that could not bind.
func (o *Overlay) Apply(s snapshot.Snapshot) error {
	peers := make(map[string]snapshot.Peer, len(s.Peers))
	for _, peer := range s.Peers {
		peers[peer.ID] = peer
	}
	routes := make(map[string]snapshot.RelayRoute, len(s.Relay))
	for _, route := range s.Relay {
		routes[route.Key] = route
	}
	o.peers.Store(&peers)
	o.routes.Store(&routes)

	type linkSpec struct {
		peer    string
		proxies []snapshot.Proxy
	}
	var wanted []linkSpec
	for _, site := range s.Sites {
		for _, upstream := range site.Upstreams {
			for _, path := range upstream.Paths {
				if path.FirstNode != "" {
					wanted = append(wanted, linkSpec{path.FirstNode, path.LinkProxies})
				}
			}
		}
	}
	for _, route := range s.Relay {
		if route.Next != "" {
			wanted = append(wanted, linkSpec{route.Next, route.LinkProxies})
		}
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	keep := make(map[string]bool, len(wanted))
	for _, spec := range wanted {
		address := peers[spec.peer].Address
		if address == "" {
			continue
		}
		key := linkKey(spec.peer, address, spec.proxies)
		keep[key] = true
		if o.links[key] == nil {
			o.links[key] = newLink(o.identity, spec.peer, address, spec.proxies, o.log)
		}
	}
	for key, l := range o.links {
		if !keep[key] {
			l.retire()
			delete(o.links, key)
		}
	}
	return o.applyRelayLocked(s)
}

func (o *Overlay) applyRelayLocked(s snapshot.Snapshot) error {
	if len(s.Relay) == 0 || s.RelayListen == "" {
		// Tunnels already open keep flowing; new ones are refused because no route allows them.
		return nil
	}
	if o.relay != nil && o.relay.address == s.RelayListen {
		return nil
	}
	relay, err := o.startRelay(s.RelayListen)
	if err != nil {
		return fmt.Errorf("relay port %s: %w", s.RelayListen, err)
	}
	if o.relay != nil {
		o.relay.drain()
	}
	o.relay = relay
	return nil
}

// DialPath connects to the path's target: through a tunnel when the path crosses nodes, otherwise directly
// or through its proxies.
func (o *Overlay) DialPath(ctx context.Context, path snapshot.Path) (net.Conn, error) {
	if path.FirstNode == "" {
		return DialChain(ctx, path.Egress, path.Target)
	}
	return o.tunnel(ctx, path.FirstNode, path.LinkProxies, path.Key)
}

func (o *Overlay) dialNext(ctx context.Context, route snapshot.RelayRoute) (net.Conn, error) {
	if route.Next == "" {
		return DialChain(ctx, route.Egress, route.Target)
	}
	return o.tunnel(ctx, route.Next, route.LinkProxies, route.Key)
}

func (o *Overlay) tunnel(ctx context.Context, peer string, proxies []snapshot.Proxy, key string) (net.Conn, error) {
	address := (*o.peers.Load())[peer].Address
	if address == "" {
		return nil, fmt.Errorf("node %s has no relay address", peer)
	}
	l := o.link(peer, address, proxies)
	cc, err := l.acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("link to %s: %w", peer, err)
	}
	header := make(http.Header, 1)
	header.Set(RouteHeader, key)
	return openTunnel(ctx, cc, peer, header)
}

func (o *Overlay) link(peer, address string, proxies []snapshot.Proxy) *link {
	key := linkKey(peer, address, proxies)
	o.mu.Lock()
	defer o.mu.Unlock()
	l := o.links[key]
	if l == nil {
		l = newLink(o.identity, peer, address, proxies, o.log)
		o.links[key] = l
	}
	return l
}

// Links reports the state of every link.
func (o *Overlay) Links() []LinkStatus {
	o.mu.Lock()
	links := make([]*link, 0, len(o.links))
	for _, l := range o.links {
		links = append(links, l)
	}
	o.mu.Unlock()
	statuses := make([]LinkStatus, 0, len(links))
	for _, l := range links {
		statuses = append(statuses, l.status())
	}
	return statuses
}

// Close closes the relay port and every link, ending all tunnels.
func (o *Overlay) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.relay != nil {
		o.relay.close()
		o.relay = nil
	}
	for key, l := range o.links {
		l.retire()
		l.mu.Lock()
		for _, cc := range l.conns {
			cc.Close()
		}
		l.mu.Unlock()
		delete(o.links, key)
	}
}

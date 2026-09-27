package overlay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/snapshot"
	"github.com/rpop-project/rpop/internal/traceid"
)

// ErrClosed is returned by an overlay's methods once Close has run: a node replaces its overlay wholesale when
// it registers again, and the old one must refuse new work rather than start links or a relay port nobody will
// ever close.
var ErrClosed = errors.New("overlay is closed")

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
	// events queues tunnel lifecycle events for asynchronous delivery; see SetTunnelEventSink.
	events *eventQueue
	closed bool
}

// New creates the overlay of the node identity names.
func New(identity *pki.Identity, log *zap.Logger) *Overlay {
	o := &Overlay{identity: identity, log: log, links: make(map[string]*link), events: newEventQueue(log)}
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
	if o.closed {
		return ErrClosed
	}
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
		// The node no longer relays: drain and forget any port it used to hold open. New tunnels are already
		// refused because no route allows them; draining lets tunnels already open keep flowing to completion
		// instead of cutting them off.
		if o.relay != nil {
			o.relay.drain()
			o.relay = nil
		}
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
// or through its proxies. When ctx carries WithTunnelLogging(true), it mints a tunnel ID, sends it and the
// logging decision on the CONNECT stream so every relay and the exit record the same tunnel (D22, P7), and
// reports its own arrived/established/ended events for this, the entry hop.
func (o *Overlay) DialPath(ctx context.Context, path snapshot.Path) (net.Conn, error) {
	if path.FirstNode == "" {
		return DialChain(ctx, path.Egress, path.Target)
	}
	open := tunnelOpen{logEvents: tunnelLoggingEnabled(ctx), reportOwnEnd: true}
	if open.logEvents {
		open.tunnelID = traceid.New()
	}
	arrived := time.Now()
	if open.logEvents {
		o.events.record(TunnelEvent{Timestamp: arrived, TunnelID: open.tunnelID, NodeID: o.identity.NodeID, Role: RoleEntry, Stage: StageArrived, Peer: path.FirstNode})
	}
	conn, err := o.tunnel(ctx, path.FirstNode, path.LinkProxies, path.Key, open)
	if err != nil {
		if open.logEvents {
			o.events.record(TunnelEvent{Timestamp: time.Now(), TunnelID: open.tunnelID, NodeID: o.identity.NodeID, Role: RoleEntry, Stage: StageEnded, Peer: path.FirstNode, Duration: time.Since(arrived), Error: err.Error()})
		}
		return nil, err
	}
	if open.logEvents {
		o.events.record(TunnelEvent{Timestamp: time.Now(), TunnelID: open.tunnelID, NodeID: o.identity.NodeID, Role: RoleEntry, Stage: StageEstablished, Peer: path.FirstNode, Duration: time.Since(arrived)})
	}
	return conn, nil
}

// dialNext connects a relay to the rest of a route: onward to another relay through a tunnel that forwards the
// tunnel ID and logging decision it received, or, at the exit, straight to the target. open.reportOwnEnd is
// always false here: serveRelay wraps the whole hop's lifetime around this call instead of letting a nested
// tunnel report its own end.
func (o *Overlay) dialNext(ctx context.Context, route snapshot.RelayRoute, open tunnelOpen) (net.Conn, error) {
	if route.Next == "" {
		return DialChain(ctx, route.Egress, route.Target)
	}
	open.reportOwnEnd = false
	return o.tunnel(ctx, route.Next, route.LinkProxies, route.Key, open)
}

// tunnelOpen carries one CONNECT dial's tracing identity: whether the tunnel it belongs to logs lifecycle
// events, the tunnel's ID, and whether this particular connection should itself report a StageEnded event when
// it closes.
type tunnelOpen struct {
	tunnelID     string
	logEvents    bool
	reportOwnEnd bool
}

func (o *Overlay) tunnel(ctx context.Context, peer string, proxies []snapshot.Proxy, key string, open tunnelOpen) (net.Conn, error) {
	address := (*o.peers.Load())[peer].Address
	if address == "" {
		return nil, fmt.Errorf("node %s has no relay address", peer)
	}
	l, err := o.link(peer, address, proxies)
	if err != nil {
		return nil, err
	}
	cc, err := l.acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("link to %s: %w", peer, err)
	}
	header := make(http.Header, 3)
	header.Set(RouteHeader, key)
	if open.logEvents {
		header.Set(TunnelIDHeader, open.tunnelID)
		header.Set(TunnelLogHeader, "1")
	}
	var trace tunnelTrace
	if open.logEvents && open.reportOwnEnd {
		trace = tunnelTrace{tunnelID: open.tunnelID, nodeID: o.identity.NodeID, role: RoleEntry, events: o.events, opened: time.Now()}
	}
	return openTunnel(ctx, cc, peer, header, trace)
}

func (o *Overlay) link(peer, address string, proxies []snapshot.Proxy) (*link, error) {
	key := linkKey(peer, address, proxies)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil, ErrClosed
	}
	l := o.links[key]
	if l == nil {
		l = newLink(o.identity, peer, address, proxies, o.log)
		o.links[key] = l
	}
	return l, nil
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

// Close closes the relay port and every link, ending all tunnels. Once it returns, every method that would
// start new work (Apply, DialPath) refuses it with ErrClosed instead of starting a link or relay port that
// this overlay, being discarded, will never close again.
func (o *Overlay) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
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

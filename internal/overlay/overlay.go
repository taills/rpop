package overlay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/sharedport"
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
	// config holds the HTTP/2 window and stream settings (D31) every link and the relay port are built with;
	// set once by New and never modified afterward, so reading it needs no lock.
	config Config
	// registry binds the relay port's TLS owner registration (PutTLSOwner, see startRelay); set once by New
	// (WithRegistry, or a private registry of its own by default — see New's doc comment) and never modified
	// afterward, so reading it needs no lock, like config.
	registry *sharedport.Registry
	peers    atomic.Pointer[map[string]snapshot.Peer]
	routes   atomic.Pointer[map[string]snapshot.RelayRoute]

	mu    sync.Mutex
	links map[string]*link
	relay *relayServer
	// events queues tunnel lifecycle events for asynchronous delivery; see SetTunnelEventSink.
	events *eventQueue
	closed bool

	// protocolVersionState backs checkTunnelProtocolVersion (D27).
	protocolVersionState
}

// Option configures an Overlay at construction time.
type Option func(*Overlay)

// WithRegistry makes the overlay bind its relay port through registry instead of the private one New creates by
// default, so the relay port can share an address with the node's own dataplane sites (internal/dataplane.Engine,
// given the same registry via WithRegistry/SetRegistry) and, when the addresses coincide, with the console or
// southbound — see internal/sharedport and docs/architecture/control-data-plane.md §5. A caller that does not
// need this (most overlay tests, and any node whose relay port never shares an address with anything else) can
// simply omit it: New's own private registry behaves exactly as a raw net.Listen-backed relay port did before
// this option existed, since nothing else ever registers on it.
func WithRegistry(registry *sharedport.Registry) Option {
	return func(o *Overlay) { o.registry = registry }
}

// New creates the overlay of the node identity names, tuned with cfg's HTTP/2 window and stream settings (D31;
// see DefaultConfig for the zero-configuration values). Its relay port binds through a private
// internal/sharedport.Registry unless an Option (WithRegistry) says otherwise.
func New(identity *pki.Identity, log *zap.Logger, cfg Config, opts ...Option) *Overlay {
	o := &Overlay{
		identity: identity, log: log, config: cfg, links: make(map[string]*link), events: newEventQueue(log),
		registry: sharedport.NewRegistry(),
	}
	for _, opt := range opts {
		opt(o)
	}
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
	previousPeers := o.peers.Swap(&peers)
	o.forgetStalePeers(previousPeers, peers)
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
		info := peers[spec.peer]
		if info.Address == "" {
			continue
		}
		key := linkKey(spec.peer, info.Generation, info.Address, spec.proxies)
		keep[key] = true
		if o.links[key] == nil {
			o.links[key] = newLink(o.identity, spec.peer, info.Address, spec.proxies, o.peerGeneration(spec.peer), o.config, o.log)
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

// forgetStalePeers drops checkTunnelProtocolVersion's per-peer warn-once bookkeeping (D27's protocolWarned, see
// ForgetPeer) for any peer ID that was in previous but is missing from current — most commonly because the
// controller deleted that node, so it no longer appears in any snapshot at all. Without this, a peer ID reused by
// a different node later (or simply reappearing once it is placed on a path again, since the peer set here is
// only "the peers this node's current snapshot mentions," not "every node that ever existed") would silently
// inherit the deleted node's already-warned state and never log its own first mismatch. previous is nil only if
// this is ever called before New's zero-value initialization, which does not happen in practice (New always
// stores an empty map first), but is handled defensively anyway. Called with no lock held; sync.Map.Delete needs
// none of its own.
func (o *Overlay) forgetStalePeers(previous *map[string]snapshot.Peer, current map[string]snapshot.Peer) {
	if previous == nil {
		return
	}
	for id := range *previous {
		if _, ok := current[id]; !ok {
			o.ForgetPeer(id)
		}
	}
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
	info := (*o.peers.Load())[peer]
	if info.Address == "" {
		return nil, fmt.Errorf("node %s has no relay address", peer)
	}
	l, err := o.link(peer, info.Address, info.Generation, proxies)
	if err != nil {
		return nil, err
	}
	cc, err := l.acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("link to %s: %w", peer, err)
	}
	header := make(http.Header, 4)
	header.Set(RouteHeader, key)
	header.Set(ProtocolVersionHeader, strconv.Itoa(ProtocolVersion))
	if open.logEvents {
		header.Set(TunnelIDHeader, open.tunnelID)
		header.Set(TunnelLogHeader, "1")
	}
	var trace tunnelTrace
	if open.logEvents && open.reportOwnEnd {
		trace = tunnelTrace{tunnelID: open.tunnelID, nodeID: o.identity.NodeID, role: RoleEntry, events: o.events, opened: time.Now()}
	}
	conn, peerVersion, err := openTunnel(ctx, cc, peer, header, trace)
	if err != nil {
		return nil, err
	}
	o.checkTunnelProtocolVersion(peer, peerVersion)
	return conn, nil
}

func (o *Overlay) link(peer, address string, generation int64, proxies []snapshot.Proxy) (*link, error) {
	key := linkKey(peer, generation, address, proxies)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil, ErrClosed
	}
	l := o.links[key]
	if l == nil {
		l = newLink(o.identity, peer, address, proxies, o.peerGeneration(peer), o.config, o.log)
		o.links[key] = l
	}
	return l, nil
}

// peerGeneration reads peer's current registration generation from the latest snapshot Apply installed. A
// link calls it on every handshake, not just when the link is created, so a peer revoked and re-registered
// after the link was dialed is rejected on its next handshake instead of trusted for the connection's life.
func (o *Overlay) peerGeneration(peer string) func() (int64, bool) {
	return func() (int64, bool) {
		p, ok := (*o.peers.Load())[peer]
		return p.Generation, ok
	}
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

// Close closes the relay port and every link, ending all tunnels, and stops the event queue's delivery
// goroutine. It waits for each link's maintain goroutine to exit before returning; retire cancels any dial
// maintain has in flight (see link.retire), so this returns promptly even if a peer is slow or unreachable
// rather than waiting out a full dial+handshake timeout on it.
func (o *Overlay) Close() {
	o.mu.Lock()
	o.closed = true
	if o.relay != nil {
		o.relay.close()
		o.relay = nil
	}
	links := make([]*link, 0, len(o.links))
	for key, l := range o.links {
		l.retire()
		l.mu.Lock()
		for _, cc := range l.conns {
			cc.Close()
		}
		l.mu.Unlock()
		links = append(links, l)
		delete(o.links, key)
	}
	o.mu.Unlock()
	// Wait outside the lock: maintain does not need o.mu, but Links and status reporting still take it, and
	// they must not block on a link that is merely finishing up its last redial attempt.
	for _, l := range links {
		<-l.done
	}
	o.events.close()
}

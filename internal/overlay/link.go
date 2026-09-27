package overlay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/snapshot"
)

const (
	linkPingAfter   = 15 * time.Second
	linkPingTimeout = 10 * time.Second
	linkIdleConns   = 1
	// Nodes apply a revision at the same moment, so a relay often dials its next hop just before that node's
	// relay port is up. A short first backoff brings the link up right after; it doubles for peers that stay down.
	minLinkBackoff = 200 * time.Millisecond
	maxLinkBackoff = 30 * time.Second
)

// Config holds the overlay's HTTP/2 flow-control window sizes and per-connection stream limit (D31): a process
// startup parameter for whichever node or embedded node runs this overlay, tuned once for a host's own
// bandwidth-delay product and connection budget rather than rendered per site or per snapshot revision (it never
// travels in a snapshot). New takes one and hands it, unmodified, to every link it dials and to its relay port, so
// building it once at construction is what makes every later read of these fields safe without a lock.
type Config struct {
	// StreamWindowBytes is the HTTP/2 flow-control window for one stream (one tunnel): the default keeps a single
	// download at line rate across a 100 ms, 1 Gbit/s path.
	StreamWindowBytes int
	// ConnectionWindowBytes is the flow-control window shared by every stream on one connection.
	ConnectionWindowBytes int
	// MaxStreamsPerConn bounds concurrent tunnels the relay port accepts on one connection; a link dialing out
	// opens another connection instead of queuing once every open one is at this limit (see link.acquire).
	MaxStreamsPerConn int
}

// Default* are Config's zero-configuration values (D31): unchanged from the constants they replace, so a process
// given no overlay configuration behaves exactly as it did before those constants became configurable.
const (
	DefaultStreamWindowBytes     = 16 << 20
	DefaultConnectionWindowBytes = 64 << 20
	DefaultMaxStreamsPerConn     = 1000
)

// Bounds a Config must satisfy (see ValidateConfig). A window below MinWindowBytes can stall a fast link's
// throughput; one above MaxWindowBytes, or a stream count above MaxStreamsPerConnLimit, risks unbounded
// per-connection memory or file-descriptor growth from a single misconfigured value.
const (
	MinWindowBytes         = 64 << 10
	MaxWindowBytes         = 256 << 20
	MinStreamsPerConn      = 1
	MaxStreamsPerConnLimit = 10000
)

// DefaultConfig is the overlay's configuration with nothing overriding it.
func DefaultConfig() Config {
	return Config{
		StreamWindowBytes: DefaultStreamWindowBytes, ConnectionWindowBytes: DefaultConnectionWindowBytes,
		MaxStreamsPerConn: DefaultMaxStreamsPerConn,
	}
}

// ValidateConfig rejects a Config outside the bounds above, so a bad CLI flag or environment variable fails at
// startup with a clear message instead of silently misconfiguring every link and relay port the process opens.
func ValidateConfig(cfg Config) error {
	if cfg.StreamWindowBytes < MinWindowBytes || cfg.StreamWindowBytes > MaxWindowBytes {
		return fmt.Errorf("overlay stream window must be between %d and %d bytes, got %d", MinWindowBytes, MaxWindowBytes, cfg.StreamWindowBytes)
	}
	if cfg.ConnectionWindowBytes < MinWindowBytes || cfg.ConnectionWindowBytes > MaxWindowBytes {
		return fmt.Errorf("overlay connection window must be between %d and %d bytes, got %d", MinWindowBytes, MaxWindowBytes, cfg.ConnectionWindowBytes)
	}
	if cfg.MaxStreamsPerConn < MinStreamsPerConn || cfg.MaxStreamsPerConn > MaxStreamsPerConnLimit {
		return fmt.Errorf("overlay max streams per connection must be between %d and %d, got %d", MinStreamsPerConn, MaxStreamsPerConnLimit, cfg.MaxStreamsPerConn)
	}
	return nil
}

// linkKey identifies a link: one peer at one registration generation, reached at one address through one proxy
// chain. Generation is part of the key so a peer that re-registers gets a brand new link, forcing a fresh,
// freshly-verified handshake instead of continuing to trust a connection opened under its previous generation.
func linkKey(peer string, generation int64, address string, proxies []snapshot.Proxy) string {
	data, _ := json.Marshal(struct {
		Peer       string
		Generation int64
		Address    string
		Proxies    []snapshot.Proxy
	}{peer, generation, address, proxies})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

// link keeps persistent HTTP/2 connections to one peer's relay port; tunnels are streams on them. It keeps at
// least one connection open at all times, so the first request never pays for TCP, proxy, and TLS handshakes,
// and opens more when every connection is at its stream limit.
type link struct {
	key, peer, address string
	// proxies is the link's proxy chain, rendered once for status reports (D15); immutable after construction.
	proxies   []string
	log       *zap.Logger
	transport *http.Transport

	// ctx is canceled by retire, so a dial already in flight in maintain aborts immediately instead of running
	// to its full dialTimeout+handshakeTimeout (up to 20s): a caller retiring or closing every link (see
	// Overlay.Close) must not be left waiting on one slow or unreachable peer.
	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	conns     []*http.ClientConn
	failures  int
	downUntil time.Time
	// lastErr is the most recent dial failure (D19); cleared on the next successful dial. lastSuccess is when a
	// connection was last established; both are for status reports only and never gate dialing decisions.
	lastErr     string
	lastSuccess time.Time
	retired     bool
	// dialing is closed when the dial in progress finishes; tunnels wait for it instead of dialing in parallel.
	dialing chan struct{}
	wake    chan struct{}
	done    chan struct{}
}

// newLink builds a link to peer, verifying on every handshake that it still presents a certificate for the
// generation currentGeneration currently reports, so a peer revoked and re-registered after this link was
// created is rejected rather than trusted for as long as the connection happens to stay open.
func newLink(identity *pki.Identity, peer, address string, proxies []snapshot.Proxy, currentGeneration func() (int64, bool), cfg Config, log *zap.Logger) *link {
	generation, _ := currentGeneration()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return DialChain(ctx, proxies, addr)
		},
		TLSClientConfig:     identity.PeerClientConfig(peer, currentGeneration),
		TLSHandshakeTimeout: handshakeTimeout,
		HTTP2: &http.HTTP2Config{
			SendPingTimeout: linkPingAfter, PingTimeout: linkPingTimeout,
			MaxReceiveBufferPerStream: cfg.StreamWindowBytes, MaxReceiveBufferPerConnection: cfg.ConnectionWindowBytes,
		},
	}
	transport.Protocols = new(http.Protocols)
	transport.Protocols.SetHTTP2(true)
	ctx, cancel := context.WithCancel(context.Background())
	l := &link{
		key: linkKey(peer, generation, address, proxies), peer: peer, address: address, proxies: ProxyChainLabels(proxies),
		transport: transport,
		log:       log.With(zap.String("peer", peer), zap.String("address", address)),
		ctx:       ctx, cancel: cancel,
		wake: make(chan struct{}, 1), done: make(chan struct{}),
	}
	go l.maintain()
	return l
}

// acquire returns a connection with a reserved stream slot, preferring the least loaded one. It dials another
// connection only when every open one is at its stream limit.
func (l *link) acquire(ctx context.Context) (*http.ClientConn, error) {
	for {
		l.mu.Lock()
		if l.retired {
			l.mu.Unlock()
			return nil, errLinkDown
		}
		l.pruneLocked()
		var best *http.ClientConn
		for _, cc := range l.conns {
			if cc.Available() > 0 && (best == nil || cc.InFlight() < best.InFlight()) {
				best = cc
			}
		}
		if best != nil && best.Reserve() == nil {
			l.mu.Unlock()
			return best, nil
		}
		if pending := l.dialing; pending != nil {
			// A link whose last dial failed is known to be down: the redial can hang until the handshake
			// times out, and the ingress should try its next path now rather than wait for it.
			down := l.failures > 0
			l.mu.Unlock()
			if down {
				return nil, errLinkDown
			}
			select {
			case <-pending:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if len(l.conns) == 0 && time.Now().Before(l.downUntil) {
			l.mu.Unlock()
			return nil, errLinkDown
		}
		finished := l.beginDialLocked()
		l.mu.Unlock()
		cc, err := l.dial(ctx, finished)
		if err != nil {
			return nil, err
		}
		if cc.Reserve() == nil {
			return cc, nil
		}
	}
}

// beginDialLocked records that a dial is starting; pass the result to dial.
func (l *link) beginDialLocked() chan struct{} {
	finished := make(chan struct{})
	l.dialing = finished
	return finished
}

func (l *link) dial(ctx context.Context, finished chan struct{}) (*http.ClientConn, error) {
	cc, err := l.transport.NewClientConn(ctx, "https", l.address)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dialing == finished {
		l.dialing = nil
	}
	close(finished)
	if err != nil {
		// A dial that failed only because the leader's own context ended says nothing about the peer: the link
		// stays up, and whichever tunnel wakes up next leads its own attempt at once, with no backoff. A dial
		// that ran out of its own bounded timeout is a real signal instead, and still counts as a failure.
		if ctx.Err() == context.Canceled {
			return nil, err
		}
		l.failures++
		l.downUntil = time.Now().Add(backoff(l.failures))
		l.lastErr = err.Error()
		return nil, err
	}
	if l.retired {
		cc.Close()
		return nil, errLinkDown
	}
	l.failures, l.downUntil, l.lastErr = 0, time.Time{}, ""
	l.lastSuccess = time.Now()
	l.conns = append(l.conns, cc)
	cc.SetStateHook(l.changed)
	return cc, nil
}

func backoff(failures int) time.Duration {
	return min(minLinkBackoff<<min(failures-1, 8), maxLinkBackoff)
}

// changed runs when a connection's load or health changes.
func (l *link) changed(*http.ClientConn) {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// pruneLocked forgets connections that can no longer carry streams.
func (l *link) pruneLocked() {
	live := l.conns[:0]
	for _, cc := range l.conns {
		if cc.Err() == nil {
			live = append(live, cc)
		}
	}
	clear(l.conns[len(live):])
	l.conns = live
}

// maintain keeps an idle connection ready, redialing with backoff, and closes a retired link's connections
// once their last tunnel ends.
func (l *link) maintain() {
	defer close(l.done)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-l.wake:
		case <-timer.C:
		}
		l.mu.Lock()
		l.pruneLocked()
		if l.retired {
			busy := false
			for _, cc := range l.conns {
				if cc.InFlight() > 0 {
					busy = true
				} else {
					cc.Close()
				}
			}
			l.pruneLocked()
			if !busy {
				l.mu.Unlock()
				return
			}
			l.mu.Unlock()
			continue
		}
		if pending := l.dialing; pending != nil {
			l.mu.Unlock()
			<-pending
			l.changed(nil)
			continue
		}
		if len(l.conns) >= linkIdleConns {
			l.mu.Unlock()
			continue
		}
		if wait := time.Until(l.downUntil); wait > 0 {
			l.mu.Unlock()
			timer.Reset(wait)
			continue
		}
		finished := l.beginDialLocked()
		l.mu.Unlock()
		ctx, cancel := context.WithTimeout(l.ctx, dialTimeout+handshakeTimeout)
		_, err := l.dial(ctx, finished)
		cancel()
		if err != nil {
			if l.ctx.Err() != nil {
				// retire canceled l.ctx mid-dial; this is a shutdown, not a real failure. The next iteration's
				// top-of-loop retired check handles closing up and returning.
				continue
			}
			l.log.Warn("overlay link is down", zap.Error(err))
			l.mu.Lock()
			wait := time.Until(l.downUntil)
			l.mu.Unlock()
			timer.Reset(max(wait, minLinkBackoff))
		}
	}
}

// retire stops new tunnels on the link and cancels any dial currently in flight in maintain, so it and callers
// waiting on done (see Overlay.Close) are not left blocked on a slow or unreachable peer. Tunnels already open
// finish on their connections.
func (l *link) retire() {
	l.mu.Lock()
	l.retired = true
	l.mu.Unlock()
	l.cancel()
	l.changed(nil)
}

// LinkStatus describes one overlay link for status reports (D15/D19, stage 6).
type LinkStatus struct {
	Peer    string `json:"peer"`
	Address string `json:"address"`
	// Proxies names the link's proxy chain in dial order, as "type://address" (see ProxyChainLabels); nil for a
	// direct node-to-node link with no intermediate proxies. Never carries credentials.
	Proxies []string `json:"proxies,omitempty"`
	// Status is "up" (at least one live connection), "dialing" (a dial is in flight and none has succeeded
	// yet), or "down" (backing off, or idle with no connection and no dial in flight).
	Status      string `json:"status"`
	Connections int    `json:"connections"`
	Tunnels     int    `json:"tunnels"`
	Failures    int    `json:"failures,omitempty"`
	// DownUntil is when the link's exponential backoff ends (RFC3339, UTC); empty when it is not backing off.
	DownUntil string `json:"downUntil,omitempty"`
	// LastError is the most recent dial failure; it never carries proxy credentials (see ProxyChainLabels and
	// throughProxy's own error formatting, which only ever wrap addresses and response status text). Cleared
	// once a dial succeeds again.
	LastError string `json:"lastError,omitempty"`
	// LastSuccess is when a connection to this peer was last established (RFC3339, UTC); empty if never.
	LastSuccess string `json:"lastSuccess,omitempty"`
}

func (l *link) status() LinkStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked()
	state := "down"
	switch {
	case len(l.conns) > 0:
		state = "up"
	case l.dialing != nil:
		state = "dialing"
	}
	status := LinkStatus{
		Peer: l.peer, Address: l.address, Proxies: l.proxies, Status: state,
		Connections: len(l.conns), Failures: l.failures,
		DownUntil: formatLinkTime(l.downUntil), LastError: l.lastErr, LastSuccess: formatLinkTime(l.lastSuccess),
	}
	for _, cc := range l.conns {
		status.Tunnels += cc.InFlight()
	}
	return status
}

// formatLinkTime renders a status timestamp as RFC3339 UTC, or "" for the zero value, so a link that never
// backed off or never succeeded omits the field entirely instead of reporting "0001-01-01T00:00:00Z".
func formatLinkTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

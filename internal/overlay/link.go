package overlay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/snapshot"
)

const (
	// Flow-control windows sized for the bandwidth-delay product of long links: a 16 MiB stream window keeps a
	// single download at line rate across a 100 ms, 1 Gbit/s path, and the connection window bounds memory.
	streamWindow     = 16 << 20
	connectionWindow = 64 << 20

	linkPingAfter   = 15 * time.Second
	linkPingTimeout = 10 * time.Second
	linkIdleConns   = 1
	// Nodes apply a revision at the same moment, so a relay often dials its next hop just before that node's
	// relay port is up. A short first backoff brings the link up right after; it doubles for peers that stay down.
	minLinkBackoff    = 200 * time.Millisecond
	maxLinkBackoff    = 30 * time.Second
	maxStreamsPerConn = 1000
)

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
	log                *zap.Logger
	transport          *http.Transport

	// ctx is canceled by retire, so a dial already in flight in maintain aborts immediately instead of running
	// to its full dialTimeout+handshakeTimeout (up to 20s): a caller retiring or closing every link (see
	// Overlay.Close) must not be left waiting on one slow or unreachable peer.
	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	conns     []*http.ClientConn
	failures  int
	downUntil time.Time
	retired   bool
	// dialing is closed when the dial in progress finishes; tunnels wait for it instead of dialing in parallel.
	dialing chan struct{}
	wake    chan struct{}
	done    chan struct{}
}

// newLink builds a link to peer, verifying on every handshake that it still presents a certificate for the
// generation currentGeneration currently reports, so a peer revoked and re-registered after this link was
// created is rejected rather than trusted for as long as the connection happens to stay open.
func newLink(identity *pki.Identity, peer, address string, proxies []snapshot.Proxy, currentGeneration func() (int64, bool), log *zap.Logger) *link {
	generation, _ := currentGeneration()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return DialChain(ctx, proxies, addr)
		},
		TLSClientConfig:     identity.PeerClientConfig(peer, currentGeneration),
		TLSHandshakeTimeout: handshakeTimeout,
		HTTP2: &http.HTTP2Config{
			SendPingTimeout: linkPingAfter, PingTimeout: linkPingTimeout,
			MaxReceiveBufferPerStream: streamWindow, MaxReceiveBufferPerConnection: connectionWindow,
		},
	}
	transport.Protocols = new(http.Protocols)
	transport.Protocols.SetHTTP2(true)
	ctx, cancel := context.WithCancel(context.Background())
	l := &link{
		key: linkKey(peer, generation, address, proxies), peer: peer, address: address, transport: transport,
		log: log.With(zap.String("peer", peer), zap.String("address", address)),
		ctx: ctx, cancel: cancel,
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
		return nil, err
	}
	if l.retired {
		cc.Close()
		return nil, errLinkDown
	}
	l.failures, l.downUntil = 0, time.Time{}
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

// LinkStatus describes one overlay link for status reports.
type LinkStatus struct {
	Peer        string `json:"peer"`
	Address     string `json:"address"`
	Connections int    `json:"connections"`
	Tunnels     int    `json:"tunnels"`
	Failures    int    `json:"failures,omitempty"`
}

func (l *link) status() LinkStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked()
	status := LinkStatus{Peer: l.peer, Address: l.address, Connections: len(l.conns), Failures: l.failures}
	for _, cc := range l.conns {
		status.Tunnels += cc.InFlight()
	}
	return status
}

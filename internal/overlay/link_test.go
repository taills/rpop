package overlay

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/snapshot"
)

// newLinkWithoutMaintain builds a link the same way newLink does but without its background maintain
// goroutine, so a test fully controls who dials and when: maintain would otherwise race a test's own acquire
// call for the leader slot on a brand new link, exactly as it may in production, which is what makes the fix
// matter but also makes an isolated test of it nondeterministic unless maintain is kept out of the way.
func newLinkWithoutMaintain(identity *pki.Identity, peer, address string, proxies []snapshot.Proxy, log *zap.Logger) *link {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return DialChain(ctx, proxies, addr)
		},
		TLSClientConfig:     identity.PeerClientConfig(peer),
		TLSHandshakeTimeout: handshakeTimeout,
		HTTP2: &http.HTTP2Config{
			SendPingTimeout: linkPingAfter, PingTimeout: linkPingTimeout,
			MaxReceiveBufferPerStream: streamWindow, MaxReceiveBufferPerConnection: connectionWindow,
		},
	}
	transport.Protocols = new(http.Protocols)
	transport.Protocols.SetHTTP2(true)
	return &link{
		key: linkKey(peer, address, proxies), peer: peer, address: address, transport: transport,
		log:  log.With(zap.String("peer", peer), zap.String("address", address)),
		wake: make(chan struct{}, 1), done: make(chan struct{}),
	}
}

// TestLinkDialDoesNotFailTheLinkOnTheLeadersCanceledContext covers the defense-in-depth half of the relay dial
// fix: even if some caller ever hands acquire a context tied to its own request, one leader's context ending
// mid-dial must not mark the link down for every other tunnel waiting on it.
func TestLinkDialDoesNotFailTheLinkOnTheLeadersCanceledContext(t *testing.T) {
	ca, err := pki.NewCA("link test CA")
	if err != nil {
		t.Fatal(err)
	}
	peerAddr := freeAddress(t)
	peer := newOverlay(t, identityFor(t, ca, "peer", 1))
	apply(t, peer, snapshot.Snapshot{NodeID: "peer", RelayListen: peerAddr,
		Relay: []snapshot.RelayRoute{{Key: "k", From: []string{"someone-else"}, Target: "unused"}}})

	// A black hole: it accepts TCP and never answers the TLS handshake, so the leader's dial stays in flight
	// until the test cancels it.
	blackHoleAddr := freeAddress(t)
	blackHole, err := net.Listen("tcp", blackHoleAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer blackHole.Close()
	var mu sync.Mutex
	var held []net.Conn
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range held {
			conn.Close()
		}
	}()
	accepted := make(chan struct{}, 1)
	go func() {
		for {
			conn, err := blackHole.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
			select {
			case accepted <- struct{}{}:
			default:
			}
		}
	}()

	l := newLinkWithoutMaintain(identityFor(t, ca, "client", 1), "peer", blackHoleAddr, nil, zap.NewNop())

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := l.acquire(leaderCtx)
		leaderDone <- err
	}()
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("the leader's dial never reached the black hole")
	}
	cancelLeader()
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader's acquire = %v, want context.Canceled", err)
	}

	// The black hole's only job was to keep the leader's dial in flight long enough to cancel it; point the
	// link at the real peer for the next attempt.
	l.address = peerAddr
	waiterCtx, cancelWaiter := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWaiter()
	cc, err := l.acquire(waiterCtx)
	if err != nil {
		t.Fatalf("acquire after the leader's context canceled = %v, want success", err)
	}
	defer cc.Close()

	if status := l.status(); status.Failures != 0 {
		t.Fatalf("link failures = %d, want 0: the leader's own cancellation must not count against the link", status.Failures)
	}
}

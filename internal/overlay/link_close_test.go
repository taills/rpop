package overlay

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/snapshot"
)

// blackHoleListener accepts TCP connections and holds them open without ever answering the TLS handshake, so a
// dial through it stays in flight until its context is canceled or it times out. This is the P5 review's exact
// repro for the CRITICAL Overlay.Close finding: a peer that only accepts and never responds.
type blackHoleListener struct {
	net.Listener
	accepted chan struct{}

	mu   sync.Mutex
	held []net.Conn
}

func startBlackHole(t *testing.T) *blackHoleListener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &blackHoleListener{Listener: listener, accepted: make(chan struct{}, 1)}
	t.Cleanup(func() {
		listener.Close()
		b.mu.Lock()
		defer b.mu.Unlock()
		for _, conn := range b.held {
			conn.Close()
		}
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			b.mu.Lock()
			b.held = append(b.held, conn)
			b.mu.Unlock()
			select {
			case b.accepted <- struct{}{}:
			default:
			}
		}
	}()
	return b
}

// TestLinkRetireCancelsInFlightMaintainDial is the direct regression test for the CRITICAL finding: maintain's
// background dial (unlike a caller-driven acquire, which already used the caller's own context) used to run on
// context.Background(), so retire (and so Overlay.Close, which waits on done) could be blocked for up to
// dialTimeout+handshakeTimeout by one unresponsive peer.
func TestLinkRetireCancelsInFlightMaintainDial(t *testing.T) {
	ca, err := pki.NewCA("link retire test CA")
	if err != nil {
		t.Fatal(err)
	}
	blackHole := startBlackHole(t)
	l := newLink(identityFor(t, ca, "client", 1), "peer", blackHole.Addr().String(), nil,
		func() (int64, bool) { return 1, true }, DefaultConfig(), zap.NewNop())

	select {
	case <-blackHole.accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("maintain never dialed the black hole")
	}

	start := time.Now()
	l.retire()
	select {
	case <-l.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("link.done did not close within 2s of retire (took at least %s); the in-flight dial was not canceled", time.Since(start))
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("retire took %s to close the link, want well under 1s", elapsed)
	}
}

// TestOverlayCloseReturnsQuicklyDialingAnUnresponsivePeer is the review's own repro at the Overlay level: Apply
// a path through a peer that only accepts and never answers TLS, then Close while maintain's first dial is
// still in flight. The review measured this blocking Close for 9.7s before the fix.
func TestOverlayCloseReturnsQuicklyDialingAnUnresponsivePeer(t *testing.T) {
	ca, err := pki.NewCA("overlay close test CA")
	if err != nil {
		t.Fatal(err)
	}
	blackHole := startBlackHole(t)
	o := New(identityFor(t, ca, "node1", 1), zap.NewNop(), DefaultConfig())
	path := snapshot.Path{Key: "k", Label: "node2", FirstNode: "node2", Target: "127.0.0.1:1"}
	if err := o.Apply(snapshot.Snapshot{NodeID: "node1", Peers: []snapshot.Peer{{ID: "node2", Address: blackHole.Addr().String(), Generation: 1}},
		Sites: []snapshot.Site{{ID: "s", Upstreams: []snapshot.Upstream{{URL: "http://example.test", Paths: []snapshot.Path{path}}}}}}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-blackHole.accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("maintain never dialed the black hole")
	}

	start := time.Now()
	o.Close()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Close took %s while a link was dialing an unresponsive peer, want well under 1s", elapsed)
	}
}

// TestOverlayCloseWithConcurrentDialPathRace exercises Close racing DialPath while a link is stuck dialing an
// unresponsive peer, under the race detector.
func TestOverlayCloseWithConcurrentDialPathRace(t *testing.T) {
	ca, err := pki.NewCA("overlay close race test CA")
	if err != nil {
		t.Fatal(err)
	}
	blackHole := startBlackHole(t)
	o := New(identityFor(t, ca, "node1", 1), zap.NewNop(), DefaultConfig())
	path := snapshot.Path{Key: "k", Label: "node2", FirstNode: "node2", Target: "127.0.0.1:1"}
	if err := o.Apply(snapshot.Snapshot{NodeID: "node1", Peers: []snapshot.Peer{{ID: "node2", Address: blackHole.Addr().String(), Generation: 1}},
		Sites: []snapshot.Site{{ID: "s", Upstreams: []snapshot.Upstream{{URL: "http://example.test", Paths: []snapshot.Path{path}}}}}}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			_, _ = o.DialPath(ctx, path)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		o.Close()
	}()
	wg.Wait()
}

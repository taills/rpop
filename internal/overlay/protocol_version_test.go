package overlay

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/snapshot"
)

// TestCheckTunnelProtocolVersionWarnsOncePerPeerAndCounts covers D27's overlay-side bookkeeping in isolation:
// a missing header or one that matches this build's version is never a mismatch; a real mismatch always
// increments protocolMismatches, but only logs a Warn the first time for a given peer.
func TestCheckTunnelProtocolVersionWarnsOncePerPeerAndCounts(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	o := &Overlay{log: zap.New(core)}
	o.checkTunnelProtocolVersion("peer-a", "")                            // no header: treated as unknown, not a mismatch
	o.checkTunnelProtocolVersion("peer-a", strconv.Itoa(ProtocolVersion)) // same version: not a mismatch
	o.checkTunnelProtocolVersion("peer-a", "999")
	o.checkTunnelProtocolVersion("peer-a", "999") // repeat from the same peer: counted, not logged again
	o.checkTunnelProtocolVersion("peer-b", "999") // a different peer: its own first-time warning

	if got := o.protocolMismatches.Load(); got != 3 {
		t.Fatalf("protocolMismatches = %d, want 3", got)
	}
	entries := logs.All()
	if len(entries) != 2 {
		t.Fatalf("logged %d warnings, want 2 (one per peer)", len(entries))
	}
}

// TestOverlayTunnelHandshakeNeverRejectsOnProtocolVersionMismatch covers D27's core availability guarantee: a
// CONNECT handshake carrying a protocol version other than the relay's own still establishes the tunnel and
// carries traffic, with the relay only counting the mismatch (see checkTunnelProtocolVersion). It reaches
// directly into the link/openTunnel machinery tunnel() normally drives, rather than through DialPath, so it can
// hand-forge a mismatched Rpop-Protocol-Version header the way a peer running a different build would send one
// — this build's own tunnel() always sends its real ProtocolVersion, so there is no other way to produce a
// mismatch from a single test binary.
func TestOverlayTunnelHandshakeNeverRejectsOnProtocolVersionMismatch(t *testing.T) {
	target := startLineEcho(t)
	c := newChain(t, target)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	l, err := c.ingress.link("node2", c.relayAddr, 1, c.path.LinkProxies)
	if err != nil {
		t.Fatal(err)
	}
	cc, err := l.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	header := make(http.Header, 2)
	header.Set(RouteHeader, c.path.Key)
	header.Set(ProtocolVersionHeader, "999")
	conn, peerVersion, err := openTunnel(ctx, cc, "node2", header, tunnelTrace{})
	if err != nil {
		t.Fatalf("tunnel with a mismatched protocol version was rejected: %v", err)
	}
	defer conn.Close()
	if want := strconv.Itoa(ProtocolVersion); peerVersion != want {
		t.Fatalf("relay's echoed version = %q, want %q", peerVersion, want)
	}
	io.WriteString(conn, "hi\n")
	if got := readLine(t, bufio.NewReader(conn)); got != "echo: hi" {
		t.Fatalf("tunnel with a mismatched version did not carry traffic: got %q", got)
	}
	if got := c.relay.protocolMismatches.Load(); got != 1 {
		t.Fatalf("relay recorded %d protocol mismatches, want 1", got)
	}
}

// TestForgetPeerClearsWarnedState covers the stage 7 review's LOW item 4 in isolation: ForgetPeer removes exactly
// the named peer's entry, leaving every other peer's untouched, and is a no-op for a peer never warned about.
func TestForgetPeerClearsWarnedState(t *testing.T) {
	o := &Overlay{log: zap.NewNop()}
	o.checkTunnelProtocolVersion("peer-a", "999")
	o.checkTunnelProtocolVersion("peer-b", "999")
	if _, warned := o.protocolWarned.Load("peer-a"); !warned {
		t.Fatal("expected peer-a to be warned before ForgetPeer")
	}

	o.ForgetPeer("peer-a")
	if _, warned := o.protocolWarned.Load("peer-a"); warned {
		t.Fatal("ForgetPeer did not clear peer-a")
	}
	if _, warned := o.protocolWarned.Load("peer-b"); !warned {
		t.Fatal("ForgetPeer cleared an unrelated peer")
	}
	o.ForgetPeer("never-warned") // must not panic
}

// TestApplyForgetsPeersRemovedFromTheSnapshot covers Apply's half of the same LOW item: a peer that disappears
// from one Apply call to the next (most commonly because the controller deleted that node) has its protocolWarned
// entry cleared, so protocolWarned cannot grow forever across a long-running node's lifetime, and a peer ID later
// reused by a different node gets its own fresh warning instead of silently inheriting the old one's state. A
// peer that is merely re-sent unchanged keeps its warned state (no spurious re-warning).
func TestApplyForgetsPeersRemovedFromTheSnapshot(t *testing.T) {
	ca, err := pki.NewCA("test")
	if err != nil {
		t.Fatal(err)
	}
	o := newOverlay(t, identityFor(t, ca, "node1", 1))
	apply(t, o, snapshot.Snapshot{Peers: []snapshot.Peer{
		{ID: "node2", Address: "127.0.0.1:1", Generation: 1},
		{ID: "node3", Address: "127.0.0.1:2", Generation: 1},
	}})
	o.checkTunnelProtocolVersion("node2", "999")
	o.checkTunnelProtocolVersion("node3", "999")

	// node2 stays in the next snapshot; node3 is gone (e.g. the controller deleted it).
	apply(t, o, snapshot.Snapshot{Peers: []snapshot.Peer{{ID: "node2", Address: "127.0.0.1:1", Generation: 1}}})
	if _, warned := o.protocolWarned.Load("node2"); !warned {
		t.Fatal("protocolWarned lost node2, which is still a peer")
	}
	if _, warned := o.protocolWarned.Load("node3"); warned {
		t.Fatal("protocolWarned still holds node3 after it was removed from the snapshot")
	}
}

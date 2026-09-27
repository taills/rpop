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

package overlay

import (
	"bufio"
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

// recordingSink collects every tunnel event it receives, for assertions.
type recordingSink struct {
	mu     sync.Mutex
	events []TunnelEvent
}

func (s *recordingSink) RecordTunnelEvent(e TunnelEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *recordingSink) snapshot() []TunnelEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TunnelEvent(nil), s.events...)
}

// waitForEvents polls sink until it holds at least n events or the deadline passes.
func waitForEvents(t *testing.T, sink *recordingSink, n int) []TunnelEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		events := sink.snapshot()
		if len(events) >= n {
			return events
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d tunnel events, got %d: %#v", n, len(events), events)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func stagesOf(events []TunnelEvent) []string {
	stages := make([]string, len(events))
	for i, e := range events {
		stages[i] = e.Stage
	}
	return stages
}

func TestTunnelEventsAreRecordedOnlyWhenTheirSiteEnabledAccessLogging(t *testing.T) {
	target := startLineEcho(t)
	c := newChain(t, target)
	ingressSink, relaySink, exitSink := &recordingSink{}, &recordingSink{}, &recordingSink{}
	c.ingress.SetTunnelEventSink(ingressSink)
	c.relay.SetTunnelEventSink(relaySink)
	c.exit.SetTunnelEventSink(exitSink)

	// Logging disabled (the default context carries no decision): the tunnel still works, but nothing is
	// recorded on any hop.
	conn, err := dial(t, c.ingress, c.path)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(conn, "ping\n")
	readLine(t, bufio.NewReader(conn))
	conn.Close()
	time.Sleep(100 * time.Millisecond)
	for name, sink := range map[string]*recordingSink{"ingress": ingressSink, "relay": relaySink, "exit": exitSink} {
		if got := sink.snapshot(); len(got) != 0 {
			t.Fatalf("%s recorded tunnel events %#v even though the site disabled access logging", name, got)
		}
	}
}

func TestTunnelEventsCarryOneTunnelIDAcrossEveryHop(t *testing.T) {
	target := startLineEcho(t)
	c := newChain(t, target)
	ingressSink, relaySink, exitSink := &recordingSink{}, &recordingSink{}, &recordingSink{}
	c.ingress.SetTunnelEventSink(ingressSink)
	c.relay.SetTunnelEventSink(relaySink)
	c.exit.SetTunnelEventSink(exitSink)

	ctx, cancel := context.WithTimeout(WithTunnelLogging(context.Background(), true), 5*time.Second)
	defer cancel()
	conn, err := c.ingress.DialPath(ctx, c.path)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(conn, "ping\n")
	if got := readLine(t, bufio.NewReader(conn)); got != "echo: ping" {
		t.Fatalf("got %q", got)
	}
	conn.Close()

	ingressEvents := waitForEvents(t, ingressSink, 3)
	relayEvents := waitForEvents(t, relaySink, 3)
	exitEvents := waitForEvents(t, exitSink, 3)

	tunnelID := ingressEvents[0].TunnelID
	if tunnelID == "" {
		t.Fatal("ingress did not mint a tunnel id")
	}
	for name, events := range map[string][]TunnelEvent{"ingress": ingressEvents, "relay": relayEvents, "exit": exitEvents} {
		for _, e := range events {
			if e.TunnelID != tunnelID {
				t.Fatalf("%s event %#v does not carry the ingress tunnel id %q", name, e, tunnelID)
			}
		}
		if got, want := stagesOf(events), []string{StageArrived, StageEstablished, StageEnded}; !equalStrings(got, want) {
			t.Fatalf("%s stages = %v, want %v", name, got, want)
		}
	}
	if ingressEvents[0].Role != RoleEntry {
		t.Fatalf("ingress role = %q, want entry", ingressEvents[0].Role)
	}
	if relayEvents[0].Role != RoleRelay {
		t.Fatalf("relay role = %q, want relay", relayEvents[0].Role)
	}
	if exitEvents[0].Role != RoleExit {
		t.Fatalf("exit role = %q, want exit", exitEvents[0].Role)
	}
	if ingressEvents[0].NodeID != "node1" || relayEvents[0].NodeID != "node2" || exitEvents[0].NodeID != "node3" {
		t.Fatalf("unexpected node ids: ingress=%s relay=%s exit=%s", ingressEvents[0].NodeID, relayEvents[0].NodeID, exitEvents[0].NodeID)
	}
}

// TestTunnelEventsIgnoreAMalformedTunnelIDButStillForward covers stage 5 security review item 4: a CONNECT
// request's Rpop-Tunnel-Id header comes from whichever peer opened the stream (relay.go trusts it as-is), so a
// buggy or compromised peer can send one that is not shaped like a UUID. Relaying the tunnel must not depend on
// it (only RouteHeader does), and neither hop past the malformed header may record an event keyed by it.
func TestTunnelEventsIgnoreAMalformedTunnelIDButStillForward(t *testing.T) {
	target := startLineEcho(t)
	c := newChain(t, target)
	relaySink, exitSink := &recordingSink{}, &recordingSink{}
	c.relay.SetTunnelEventSink(relaySink)
	c.exit.SetTunnelEventSink(exitSink)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Calling tunnel directly, instead of DialPath (which always mints a valid ID itself), simulates a peer that
	// does not go through this package's own honest header construction.
	conn, err := c.ingress.tunnel(ctx, c.path.FirstNode, c.path.LinkProxies, c.path.Key, tunnelOpen{tunnelID: "../../etc/passwd", logEvents: true, reportOwnEnd: true})
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(conn, "ping\n")
	if got := readLine(t, bufio.NewReader(conn)); got != "echo: ping" {
		t.Fatalf("got %q, want the tunnel to keep forwarding despite the malformed tunnel id", got)
	}
	conn.Close()
	time.Sleep(100 * time.Millisecond)

	for name, sink := range map[string]*recordingSink{"relay": relaySink, "exit": exitSink} {
		if got := sink.snapshot(); len(got) != 0 {
			t.Fatalf("%s recorded tunnel events %#v for a malformed tunnel id, want none", name, got)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

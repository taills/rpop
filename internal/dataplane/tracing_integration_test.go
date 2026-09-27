package dataplane

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/snapshot"
)

// tracingIdentity issues a node certificate straight from a fresh CA, mirroring overlay's own test helper; it
// is duplicated here because dataplane and overlay tests cannot share unexported helpers across packages.
func tracingIdentity(t *testing.T, ca *pki.CA, id string) *pki.Identity {
	t.Helper()
	keyPEM, csrPEM, err := pki.NewKeyAndCSR(id)
	if err != nil {
		t.Fatal(err)
	}
	_, certPEM, err := ca.SignNode(csrPEM, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := pki.LoadIdentity(certPEM, keyPEM, ca.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func tracingFreeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

// recordingTunnelSink is a minimal overlay.TunnelEventSink for assertions.
type recordingTunnelSink struct {
	mu     sync.Mutex
	events []overlay.TunnelEvent
}

func (s *recordingTunnelSink) RecordTunnelEvent(e overlay.TunnelEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *recordingTunnelSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

func (s *recordingTunnelSink) snapshot() []overlay.TunnelEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]overlay.TunnelEvent(nil), s.events...)
}

// TestRequestThroughOverlayCarriesOneTrackAndTunnelIDAcrossEveryHop is the end-to-end check the stage 5 design
// asks for: a real HTTP request into a site whose upstream path crosses an ingress, a relay, and an exit node
// over the overlay must (1) reach the upstream tagged with the ingress's freshly minted Rpop-Track-Id, (2) have
// that same track ID and the tunnel ID the connection used in the access log record, and (3) have the relay
// and exit report tunnel lifecycle events tagged with that very tunnel ID.
func TestRequestThroughOverlayCarriesOneTrackAndTunnelIDAcrossEveryHop(t *testing.T) {
	ca, err := pki.NewCA("dataplane tracing test CA")
	if err != nil {
		t.Fatal(err)
	}
	var receivedTrackID string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedTrackID = r.Header.Get(TrackIDHeader)
	}))
	defer upstream.Close()
	target := strings.TrimPrefix(upstream.URL, "http://")

	relayAddr, exitAddr := tracingFreeAddress(t), tracingFreeAddress(t)
	const key = "route-1"

	exit := overlay.New(tracingIdentity(t, ca, "exit"), zap.NewNop())
	defer exit.Close()
	if err := exit.Apply(snapshot.Snapshot{NodeID: "exit", Peers: []snapshot.Peer{{ID: "relay", Generation: 1}}, RelayListen: exitAddr,
		Relay: []snapshot.RelayRoute{{Key: key, From: []string{"relay"}, Target: target}}}); err != nil {
		t.Fatal(err)
	}

	relay := overlay.New(tracingIdentity(t, ca, "relay"), zap.NewNop())
	defer relay.Close()
	if err := relay.Apply(snapshot.Snapshot{NodeID: "relay",
		Peers:       []snapshot.Peer{{ID: "ingress", Generation: 1}, {ID: "exit", Address: exitAddr, Generation: 1}},
		RelayListen: relayAddr, Relay: []snapshot.RelayRoute{{Key: key, From: []string{"ingress"}, Next: "exit", Target: target}}}); err != nil {
		t.Fatal(err)
	}

	path := snapshot.Path{Key: key, Label: "relay", FirstNode: "relay", Target: target}
	ingress := overlay.New(tracingIdentity(t, ca, "ingress"), zap.NewNop())
	defer ingress.Close()
	if err := ingress.Apply(snapshot.Snapshot{NodeID: "ingress", Peers: []snapshot.Peer{{ID: "relay", Address: relayAddr, Generation: 1}},
		Sites: []snapshot.Site{{ID: "s", Upstreams: []snapshot.Upstream{{URL: upstream.URL, Paths: []snapshot.Path{path}}}}}}); err != nil {
		t.Fatal(err)
	}

	relaySink, exitSink := &recordingTunnelSink{}, &recordingTunnelSink{}
	relay.SetTunnelEventSink(relaySink)
	exit.SetTunnelEventSink(exitSink)

	engine := newTestEngine(t)
	engine.SetPathDialer(ingress)
	writer := &fakeAccessLogWriter{}
	engine.SetAccessLogWriter(writer)
	site := snapshot.Site{ID: "s", AccessLog: snapshot.AccessLog{AdapterID: "default"},
		Upstreams: []snapshot.Upstream{{URL: upstream.URL, Paths: []snapshot.Path{path}}}}
	handler, transports, err := engine.siteHandler(site)
	if err != nil {
		t.Fatal(err)
	}

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if err := engine.DrainAccessLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The connection stays pooled for reuse; closing it explicitly is what makes the tunnel's StageEnded event
	// fire on every hop, just like a site reload or shutdown would in production (see siteRuntime.release).
	closeIdle(transports)

	record := writer.last()
	if record.TrackID == "" || record.TrackID != receivedTrackID {
		t.Fatalf("access log track id %q does not match what the upstream received %q", record.TrackID, receivedTrackID)
	}
	if record.TunnelID == "" {
		t.Fatal("access log entry has no tunnel id even though the request crossed the overlay")
	}

	deadline := time.Now().Add(3 * time.Second)
	for relaySink.count() < 3 || exitSink.count() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("relay/exit did not report 3 tunnel events each: relay=%d exit=%d", relaySink.count(), exitSink.count())
		}
		time.Sleep(5 * time.Millisecond)
	}
	for name, events := range map[string][]overlay.TunnelEvent{"relay": relaySink.snapshot(), "exit": exitSink.snapshot()} {
		for _, e := range events {
			if e.TunnelID != record.TunnelID {
				t.Fatalf("%s event %#v does not carry the access log's tunnel id %q", name, e, record.TunnelID)
			}
		}
	}
}

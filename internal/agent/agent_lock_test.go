package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/snapshot"
)

func testIdentity(t *testing.T, ca *pki.CA, id string, generation int64) *pki.Identity {
	t.Helper()
	keyPEM, csrPEM, err := pki.NewKeyAndCSR(id)
	if err != nil {
		t.Fatal(err)
	}
	_, certPEM, err := ca.SignNode(csrPEM, id, generation)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := pki.LoadIdentity(certPEM, keyPEM, ca.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

// blockingTunnelEventSink lets a test hold the overlay's event queue loop goroutine inside RecordTunnelEvent,
// so closing that overlay (which waits for the loop to exit) stays blocked for exactly as long as the test
// wants — a deterministic stand-in for "the old overlay's Close is slow" that does not depend on network
// timing, so the assertion below is not incidentally satisfied by the overlay package's own dial-cancellation
// fix (see internal/overlay/link.go's retire).
type blockingTunnelEventSink struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingTunnelEventSink() *blockingTunnelEventSink {
	return &blockingTunnelEventSink{started: make(chan struct{}), release: make(chan struct{})}
}

func (s *blockingTunnelEventSink) RecordTunnelEvent(overlay.TunnelEvent) {
	s.once.Do(func() { close(s.started) })
	<-s.release
}

// TestUseReleasesTheLockBeforeClosingTheOldOverlay is the regression test for agent.use holding a.mu while it
// closed the previous overlay: DialPath (agentPaths, which needs a.mu) must not stall for as long as closing
// the discarded overlay takes. It pins the old overlay's Close on a blocking tunnel event sink instead of a
// slow/unresponsive network peer, so it exercises agent.use's own locking independently of overlay.Close's own
// (already fixed) responsiveness to a slow peer.
func TestUseReleasesTheLockBeforeClosingTheOldOverlay(t *testing.T) {
	ca, err := pki.NewCA("agent use lock test CA")
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{ControllerURL: "https://localhost:1", DataDir: t.TempDir()}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	a.use(testIdentity(t, ca, "node1", 1))

	sink := newBlockingTunnelEventSink()
	a.mu.Lock()
	old := a.overlay
	a.mu.Unlock()
	old.SetTunnelEventSink(sink)

	// Queue one tunnel event so the event queue's loop goroutine is the one that ends up blocked inside the
	// sink; DialPath records a StageArrived event before it ever needs the (nonexistent) peer to be reachable.
	go func() {
		_, _ = old.DialPath(overlay.WithTunnelLogging(context.Background(), true),
			snapshot.Path{Key: "k", Label: "peer", FirstNode: "peer", Target: "127.0.0.1:1"})
	}()
	select {
	case <-sink.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the blocking sink never started; DialPath did not record a tunnel event")
	}

	useDone := make(chan struct{})
	go func() {
		a.use(testIdentity(t, ca, "node2", 1))
		close(useDone)
	}()
	// Give the use() goroutine a moment to reach (and, on the pre-fix code, get stuck inside) the old overlay's
	// Close before checking that a.mu is still free.
	time.Sleep(20 * time.Millisecond)
	select {
	case <-useDone:
		t.Fatal("use() returned before the blocking sink was released; it should still be closing the old overlay")
	default:
	}

	dialDone := make(chan error, 1)
	go func() {
		_, err := (agentPaths{a}).DialPath(context.Background(), snapshot.Path{Target: "127.0.0.1:1"})
		dialDone <- err
	}()
	select {
	case <-dialDone:
	case <-time.After(time.Second):
		t.Fatal("DialPath (needs a.mu) was blocked while use() was still closing the old overlay: a.mu is held too long")
	}

	close(sink.release)
	select {
	case <-useDone:
	case <-time.After(2 * time.Second):
		t.Fatal("use() never finished after the blocking sink was released")
	}
}

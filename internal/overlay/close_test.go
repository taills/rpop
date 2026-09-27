package overlay

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/snapshot"
)

// closeTestFixture applies a snapshot that gives the overlay a peer and a path through it, so DialPath reaches
// past the "no relay address" check and into the link machinery Close must guard.
func closeTestFixture(t *testing.T) (*Overlay, snapshot.Path) {
	t.Helper()
	ca, err := pki.NewCA("overlay test CA")
	if err != nil {
		t.Fatal(err)
	}
	o := New(identityFor(t, ca, "node1", 1), zap.NewNop())
	path := snapshot.Path{Key: "k", Label: "node2", FirstNode: "node2", Target: "127.0.0.1:1"}
	err = o.Apply(snapshot.Snapshot{NodeID: "node1", Peers: []snapshot.Peer{{ID: "node2", Address: freeAddress(t), Generation: 1}},
		Sites: []snapshot.Site{{ID: "s", Upstreams: []snapshot.Upstream{{URL: "http://example.test", Paths: []snapshot.Path{path}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return o, path
}

// TestOverlayRejectsWorkAfterClose covers the TOCTOU a node hits when it replaces its overlay after being
// revoked and re-registered: a caller that grabbed the old *Overlay just before Close must not be able to spin
// up a new link or relay on it afterwards, since nothing will ever close what it creates.
func TestOverlayRejectsWorkAfterClose(t *testing.T) {
	o, path := closeTestFixture(t)
	o.Close()

	before := runtime.NumGoroutine()
	if _, err := o.DialPath(context.Background(), path); !errors.Is(err, ErrClosed) {
		t.Fatalf("DialPath after Close = %v, want ErrClosed", err)
	}
	// Give a leaked link's maintain goroutine time to start, if the close check did not catch it.
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutines grew from %d to %d: DialPath after Close must not start a new link", before, after)
	}
	if got := o.Links(); len(got) != 0 {
		t.Fatalf("links after a closed overlay's DialPath = %#v, want none", got)
	}

	if err := o.Apply(snapshot.Snapshot{NodeID: "node1", RelayListen: freeAddress(t),
		Relay: []snapshot.RelayRoute{{Key: "k", From: []string{"someone"}, Target: "unused"}}}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Apply after Close = %v, want ErrClosed", err)
	}
}

// TestOverlayCloseRacesDialPath exercises Close running concurrently with DialPath under the race detector: a
// caller may hold a reference to an overlay another goroutine is closing at the same time.
func TestOverlayCloseRacesDialPath(t *testing.T) {
	o, path := closeTestFixture(t)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
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

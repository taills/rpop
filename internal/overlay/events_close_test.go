package overlay

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/pki"
)

// TestEventQueueCloseStopsLoopGoroutine covers the MEDIUM finding that closing an overlay never stopped its
// event queue's loop goroutine: every re-registration builds a new Overlay (and so a new eventQueue), and the
// old loop leaked forever, blocked ranging over a channel nobody would ever close or send on again. The review's
// repro was 20 rounds of New/Close leaking 20 goroutines.
func TestEventQueueCloseStopsLoopGoroutine(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()
	const rounds = 20
	for i := 0; i < rounds; i++ {
		q := newEventQueue(zap.NewNop())
		q.record(TunnelEvent{TunnelID: "x"})
		q.close()
	}
	if after := waitForGoroutineCount(before, 2*time.Second); after > before {
		t.Fatalf("goroutines grew from %d to %d after %d newEventQueue/close rounds, want each loop to exit", before, after, rounds)
	}
}

// TestEventQueueCloseIsSafeWithConcurrentEmit covers the "close must not panic a concurrent emit" requirement:
// record must never send on a closed channel, and once closed it should count further events as dropped
// instead of silently accepting or losing track of them. Run with -race.
func TestEventQueueCloseIsSafeWithConcurrentEmit(t *testing.T) {
	q := newEventQueue(zap.NewNop())
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					q.record(TunnelEvent{TunnelID: "concurrent"})
				}
			}
		}()
	}
	time.Sleep(10 * time.Millisecond)
	q.close()
	close(stop)
	wg.Wait()
	// A handful more, now well after close, must be counted as dropped rather than panic or block.
	before := q.dropped.Load()
	q.record(TunnelEvent{TunnelID: "after-close"})
	if q.dropped.Load() != before+1 {
		t.Fatalf("dropped count after close = %d, want %d", q.dropped.Load(), before+1)
	}
}

// TestOverlayCloseStopsEventQueueGoroutine is the review's own repro at the Overlay level: 20 rounds of New then
// Close must not leak a goroutine per round.
func TestOverlayCloseStopsEventQueueGoroutine(t *testing.T) {
	ca, err := pki.NewCA("overlay events close test CA")
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	before := runtime.NumGoroutine()
	const rounds = 20
	for i := 0; i < rounds; i++ {
		o := New(identityFor(t, ca, "node1", 1), zap.NewNop(), DefaultConfig())
		o.Close()
	}
	if after := waitForGoroutineCount(before, 2*time.Second); after > before {
		t.Fatalf("goroutines grew from %d to %d after %d New/Close rounds, want each event queue loop to exit", before, after, rounds)
	}
}

// waitForGoroutineCount polls runtime.NumGoroutine until it drops to at most before, or budget elapses,
// returning whatever it last observed: background goroutines (GC, finalizers) can take a moment to settle, so a
// single immediate reading is prone to false failures.
func waitForGoroutineCount(before int, budget time.Duration) int {
	deadline := time.Now().Add(budget)
	after := before
	for {
		runtime.GC()
		after = runtime.NumGoroutine()
		if after <= before || time.Now().After(deadline) {
			return after
		}
		time.Sleep(10 * time.Millisecond)
	}
}

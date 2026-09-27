package overlay

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestEventQueueRecordDoesNotBlockDuringCloseDrain covers P8 (logging/tracing must never block the forwarding
// path): once loop is stuck delivering a buffered event to a slow sink (see blockingSink in events_test.go), a
// concurrent record call must still return immediately and count as dropped, rather than wait for close's
// drain-and-exit sequence to finish. Before this fix, close held closeMu's write lock for that entire sequence
// (mark closed, stop loop, wait for it to drain and exit), so every concurrent record call blocked behind it for
// as long as the sink took — exactly the forwarding-path stall P8 forbids.
func TestEventQueueRecordDoesNotBlockDuringCloseDrain(t *testing.T) {
	q := newEventQueue(zap.NewNop())
	sink := &blockingSink{started: make(chan struct{}), release: make(chan struct{})}
	q.setSink(sink)

	q.record(TunnelEvent{TunnelID: "blocks-loop"})
	select {
	case <-sink.started:
	case <-time.After(time.Second):
		t.Fatal("loop never reached the blocking sink")
	}
	// loop is now stuck inside deliver -> sink.RecordTunnelEvent, so it cannot yet observe q.stop or close q.done.

	closeDone := make(chan struct{})
	go func() {
		q.close()
		close(closeDone)
	}()
	// close's write-lock hold (mark closed) is a bare bool assignment; a generous window here is still far more
	// than it could ever need, and confirms it has run before we probe for the bug this test guards against.
	time.Sleep(20 * time.Millisecond)

	select {
	case <-closeDone:
		t.Fatal("close returned while loop was still stuck in the blocking sink")
	default:
	}

	recordDone := make(chan struct{})
	go func() {
		q.record(TunnelEvent{TunnelID: "after-close-started"})
		close(recordDone)
	}()
	select {
	case <-recordDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("record blocked while close was still draining a slow sink, want it to return immediately")
	}

	if dropped := q.dropped.Load(); dropped != 1 {
		t.Fatalf("dropped = %d, want 1 (record after close started must be dropped, not blocked or delivered)", dropped)
	}

	close(sink.release)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("close never returned after its blocking sink call finished")
	}
}

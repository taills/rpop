package overlay

import (
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// countingSink counts delivered events without blocking loop, so tests can assert every event that was not
// dropped actually reached deliver().
type countingSink struct {
	mu    sync.Mutex
	count int
}

func (s *countingSink) RecordTunnelEvent(TunnelEvent) {
	s.mu.Lock()
	s.count++
	s.mu.Unlock()
}

func (s *countingSink) get() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// TestEventQueueRecordCloseMutualExclusion is the regression test for the TOCTOU between record and close: record
// checks q.closed, then separately attempts its send; close marks closed, stops loop, and waits for it to drain.
// Before the fix, close's whole sequence could complete in between record's check and its send, so the event
// landed in a channel loop had already stopped reading — neither delivered nor counted as dropped. recordSyncHook
// pins a record call inside its critical section (right after the closed check) so this test can prove close
// cannot proceed until that record call has actually enqueued its event.
func TestEventQueueRecordCloseMutualExclusion(t *testing.T) {
	q := newEventQueue(zap.NewNop())
	sink := &countingSink{}
	q.setSink(sink)

	reached := make(chan struct{})
	release := make(chan struct{})
	recordSyncHook = func() {
		close(reached)
		<-release
	}
	defer func() { recordSyncHook = nil }()

	recordDone := make(chan struct{})
	go func() {
		q.record(TunnelEvent{TunnelID: "in-flight"})
		close(recordDone)
	}()
	select {
	case <-reached:
	case <-time.After(time.Second):
		t.Fatal("record never reached the hook")
	}

	closeDone := make(chan struct{})
	go func() {
		q.close()
		close(closeDone)
	}()

	// close must block until the in-flight record call releases closeMu's read lock (see the hook above): give
	// it a generous window to (wrongly) finish anyway before concluding the mutex is doing its job.
	select {
	case <-closeDone:
		t.Fatal("close finished while a record call was still mid-flight inside the closed-check/send critical section")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case <-recordDone:
	case <-time.After(time.Second):
		t.Fatal("record never returned after the hook was released")
	}
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("close never returned after the in-flight record finished")
	}

	if dropped := q.dropped.Load(); dropped != 0 {
		t.Fatalf("dropped = %d, want 0 (the in-flight record should have been enqueued and drained by loop before close finished)", dropped)
	}
	if count := sink.get(); count != 1 {
		t.Fatalf("delivered = %d, want 1 (the in-flight event must reach the sink, not vanish in an abandoned buffer)", count)
	}
}

// TestEventQueueRecordCloseRace is a concurrent stress test for the same invariant: every record call, racing a
// concurrent close, must be accounted for exactly once — either delivered or counted as dropped, never both and
// never neither. Run with -race.
func TestEventQueueRecordCloseRace(t *testing.T) {
	for round := 0; round < 50; round++ {
		q := newEventQueue(zap.NewNop())
		sink := &countingSink{}
		q.setSink(sink)

		const producers = 8
		var wg sync.WaitGroup
		wg.Add(producers + 1)
		for i := 0; i < producers; i++ {
			go func() {
				defer wg.Done()
				for j := 0; j < 20; j++ {
					q.record(TunnelEvent{TunnelID: "race"})
				}
			}()
		}
		go func() {
			defer wg.Done()
			q.close()
		}()
		wg.Wait()

		want := producers * 20
		got := sink.get() + int(q.dropped.Load())
		if got != want {
			t.Fatalf("round %d: delivered(%d)+dropped(%d) = %d, want %d (every record call must be accounted for exactly once)", round, sink.get(), q.dropped.Load(), got, want)
		}
	}
}

package overlay

import (
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// blockingSink lets a test hold the event queue's single consumer goroutine inside RecordTunnelEvent, so events
// queued behind it pile up and the queue's capacity can be exhausted deterministically.
type blockingSink struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once

	mu     sync.Mutex
	events []TunnelEvent
}

func (s *blockingSink) RecordTunnelEvent(e TunnelEvent) {
	s.once.Do(func() { close(s.started) })
	<-s.release
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

func (s *blockingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

func TestEventQueueDropsExcessEventsWithoutBlockingTheProducer(t *testing.T) {
	q := newEventQueue(zap.NewNop())
	sink := &blockingSink{started: make(chan struct{}), release: make(chan struct{})}
	q.setSink(sink)

	// The first event is picked up by the queue's loop and blocks inside RecordTunnelEvent, so every event
	// after it has to wait in the channel buffer.
	q.record(TunnelEvent{TunnelID: "first"})
	select {
	case <-sink.started:
	case <-time.After(time.Second):
		t.Fatal("the sink never started processing the first event")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < tunnelEventQueueLength+10; i++ {
			q.record(TunnelEvent{TunnelID: "overflow"})
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("record blocked while the queue was full; it must drop instead (P8)")
	}
	close(sink.release)

	if dropped := q.dropped.Load(); dropped == 0 {
		t.Fatal("expected the queue to have dropped some events once it filled up")
	}
}

func TestEventQueueFallsBackToTheLoggerWithoutASink(t *testing.T) {
	q := newEventQueue(zap.NewNop())
	// No sink installed: record must still return immediately and not panic.
	done := make(chan struct{})
	go func() {
		q.record(TunnelEvent{TunnelID: "no-sink"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("record blocked with no sink installed")
	}
}

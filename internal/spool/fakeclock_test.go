package spool

import (
	"sync"
	"time"
)

// fakeClock is a controllable clock for tests: Now() only advances when the test calls Advance, and After
// returns a channel that fires once Now() reaches (or passes) the time it was requested at. It lets tests drive
// segment-age rotation, quota-warning throttling, upload backoff, and rate limiting deterministically, without
// a real sleep.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
	// armed, if set (see notifyArmed), receives a value each time After registers a new pending waiter (never
	// for a duration that has already elapsed). A test polling some other, unrelated state (e.g. LastError) to
	// decide when to call Advance can otherwise race a goroutine that hasn't reached its next After call yet:
	// the goroutine's fresh waiter is then armed against a clock that has already moved past it, with no further
	// Advance ever coming to fire it. Waiting to receive from armed first makes "the goroutine is now waiting on
	// the clock" an observed fact instead of an assumption.
	armed chan struct{}
}

type fakeWaiter struct {
	at time.Time
	c  chan time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

// notifyArmed arms f to send on notify every time After registers a new pending waiter, from then on; see the
// armed field's doc comment. Call before starting whatever goroutine will call After.
func (f *fakeClock) notifyArmed(notify chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.armed = notify
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	c := make(chan time.Time, 1)
	at := f.now.Add(d)
	if !at.After(f.now) {
		f.mu.Unlock()
		c <- at
		return c
	}
	f.waiters = append(f.waiters, fakeWaiter{at: at, c: c})
	armed := f.armed
	f.mu.Unlock()
	if armed != nil {
		armed <- struct{}{}
	}
	return c
}

// Advance moves the fake clock forward by d, firing every pending After channel whose deadline that reaches.
func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	remaining := f.waiters[:0]
	for _, w := range f.waiters {
		if !w.at.After(f.now) {
			w.c <- f.now
			continue
		}
		remaining = append(remaining, w)
	}
	f.waiters = remaining
}

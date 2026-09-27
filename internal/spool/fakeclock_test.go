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
}

type fakeWaiter struct {
	at time.Time
	c  chan time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := make(chan time.Time, 1)
	at := f.now.Add(d)
	if !at.After(f.now) {
		c <- at
		return c
	}
	f.waiters = append(f.waiters, fakeWaiter{at: at, c: c})
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

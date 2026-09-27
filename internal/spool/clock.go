package spool

import (
	"context"
	"time"
)

// clock lets tests substitute time so segment-age rotation, quota-warning throttling, upload retry backoff, and
// rate limiting never have to sleep in real time.
type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// realClock is the production clock.
type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// sleepClock waits for d or for ctx to end, whichever comes first, using clk instead of the real wall clock. It
// reports whether it was d that elapsed (false means ctx ended first).
func sleepClock(ctx context.Context, clk clock, d time.Duration) bool {
	select {
	case <-clk.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

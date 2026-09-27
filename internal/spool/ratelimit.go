package spool

import (
	"context"
	"sync"
	"time"
)

// tokenBucket throttles the uploader's reads to a steady byte rate (D25: "转发业务永远优先", forwarding always
// wins). Tokens refill continuously from elapsed wall time rather than in discrete ticks, so a wait for a few
// bytes never has to sit out a whole refill interval; requests larger than the bucket's burst size just wait
// longer for enough tokens to accumulate.
type tokenBucket struct {
	mu      sync.Mutex
	rate    float64 // bytes per second
	burst   float64 // max bytes that can accumulate unused
	tokens  float64
	updated time.Time
	clock   clock
}

func newTokenBucket(ratePerSecond int64, clk clock) *tokenBucket {
	rate := float64(ratePerSecond)
	return &tokenBucket{rate: rate, burst: rate, tokens: rate, updated: clk.Now(), clock: clk}
}

// wait blocks until n bytes' worth of tokens are available (or ctx ends), then consumes them. need is computed
// against the *uncapped* accumulation since the last update, so a request larger than the burst size still
// only waits exactly as long as its actual deficit requires; only what is left over unspent gets capped at
// burst, since that (not a single caller's request) is what the burst limit is meant to bound.
func (b *tokenBucket) wait(ctx context.Context, n int64) error {
	for {
		b.mu.Lock()
		now := b.clock.Now()
		available := b.tokens
		if elapsed := now.Sub(b.updated).Seconds(); elapsed > 0 {
			available += elapsed * b.rate
		}
		need := float64(n) - available
		if need <= 0 {
			b.tokens = min(available-float64(n), b.burst)
			b.updated = now
			b.mu.Unlock()
			return nil
		}
		b.tokens = min(available, b.burst)
		b.updated = now
		wait := time.Duration(need / b.rate * float64(time.Second))
		b.mu.Unlock()
		if wait <= 0 {
			wait = time.Millisecond
		}
		select {
		case <-b.clock.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

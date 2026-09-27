package spool

import (
	"context"
	"testing"
	"time"
)

func TestTokenBucketAllowsBurstWithoutWaiting(t *testing.T) {
	clk := newFakeClock(time.Unix(1_700_000_000, 0))
	b := newTokenBucket(10, clk) // 10 bytes/second, burst 10.
	done := make(chan error, 1)
	go func() { done <- b.wait(context.Background(), 10) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait blocked on a request within the burst size")
	}
}

func TestTokenBucketWaitsForRefillBeyondBurst(t *testing.T) {
	clk := newFakeClock(time.Unix(1_700_000_000, 0))
	b := newTokenBucket(10, clk) // 10 bytes/second, burst 10.
	done := make(chan error, 1)
	go func() { done <- b.wait(context.Background(), 15) }() // needs 5 more bytes than the initial burst.

	select {
	case err := <-done:
		t.Fatalf("wait returned early (err=%v) before the extra tokens could have refilled", err)
	case <-time.After(50 * time.Millisecond):
	}

	clk.Advance(200 * time.Millisecond) // still short of the 500ms needed to refill the missing 5 bytes at 10/s.
	select {
	case err := <-done:
		t.Fatalf("wait returned early (err=%v) after only part of the needed refill time", err)
	case <-time.After(50 * time.Millisecond):
	}

	clk.Advance(400 * time.Millisecond) // 600ms total, comfortably past the 500ms the remaining 5 bytes need.
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait never returned after enough simulated time passed")
	}
}

func TestTokenBucketWaitRespectsContextCancellation(t *testing.T) {
	clk := newFakeClock(time.Unix(1_700_000_000, 0))
	b := newTokenBucket(1, clk) // 1 byte/second: a large request waits a long (simulated) time.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.wait(ctx, 1000) }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected wait to report the cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("wait ignored context cancellation")
	}
}

package control

import (
	"testing"
	"time"
)

// TestNodeRateLimiterAllowsABurstThenThrottlesAndRefills covers stage 5 security review item 5: each node gets
// its own bucket (so one node's upload rate never throttles another's), a fresh bucket starts full (so a node's
// very first upload is never throttled just for having started idle), and tokens refill continuously from
// elapsed wall time once spent.
func TestNodeRateLimiterAllowsABurstThenThrottlesAndRefills(t *testing.T) {
	l := newNodeRateLimiter(100) // 100 bytes/second, burst = 100 bytes
	if !l.allow("edge-1", 100) {
		t.Fatal("expected the initial burst to admit exactly one second's worth")
	}
	if l.allow("edge-1", 1) {
		t.Fatal("expected the bucket to be empty right after spending the whole burst")
	}
	if !l.allow("edge-2", 100) {
		t.Fatal("a different node must have its own, still-full bucket")
	}
	time.Sleep(150 * time.Millisecond)
	if !l.allow("edge-1", 10) {
		t.Fatal("expected edge-1's bucket to have refilled at least 10 bytes after 150ms at 100 bytes/second")
	}
}

// TestNodeRateLimiterForgetDropsANodesBucket checks the same cleanup keyedMutex.forget provides, so a
// long-lived controller does not keep one bucket per node ID that ever existed.
func TestNodeRateLimiterForgetDropsANodesBucket(t *testing.T) {
	l := newNodeRateLimiter(100)
	if !l.allow("edge-1", 100) {
		t.Fatal("expected the initial burst to admit exactly one second's worth")
	}
	l.forget("edge-1")
	if !l.allow("edge-1", 100) {
		t.Fatal("expected a forgotten node to get a fresh, full bucket on its next request")
	}
}

package control

import (
	"testing"
	"time"

	"github.com/rpop-project/rpop/internal/southbound"
)

// TestNodeRateLimiterBucketAlgorithm covers the bucket algorithm itself (fresh-full, refill, cap-at-burst,
// per-node isolation) with small, fast numbers via newNodeRateLimiterWithBurst, independent of the production
// burst floor newNodeRateLimiter applies (see TestNodeRateLimiterBurstFloorsAtMaxLogSegmentBytes for that).
func TestNodeRateLimiterBucketAlgorithm(t *testing.T) {
	t.Run("allows a burst then throttles and refills", func(t *testing.T) {
		l := newNodeRateLimiterWithBurst(100, 100) // 100 bytes/second, burst = 100 bytes
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
	})

	t.Run("forget drops a node's bucket", func(t *testing.T) {
		l := newNodeRateLimiterWithBurst(100, 100)
		if !l.allow("edge-1", 100) {
			t.Fatal("expected the initial burst to admit exactly one second's worth")
		}
		l.forget("edge-1")
		if !l.allow("edge-1", 100) {
			t.Fatal("expected a forgotten node to get a fresh, full bucket on its next request")
		}
	})
}

// TestNodeRateLimiterBurstFloorsAtMaxLogSegmentBytes documents and verifies the clamp newNodeRateLimiter applies:
// burst is always at least southbound.MaxLogSegmentBytes, regardless of the configured rate, including the
// shipped default rate (16MiB/s), which is itself smaller than MaxLogSegmentBytes (32MiB).
func TestNodeRateLimiterBurstFloorsAtMaxLogSegmentBytes(t *testing.T) {
	tests := []struct {
		name      string
		rate      int64
		wantBurst int64
	}{
		{"a rate well below MaxLogSegmentBytes clamps burst up to it", 1 << 20, southbound.MaxLogSegmentBytes},
		{"the shipped default rate still clamps burst up to it", DefaultLogIngestRateBytesPerSecond, southbound.MaxLogSegmentBytes},
		{"a rate above MaxLogSegmentBytes leaves burst at the rate", southbound.MaxLogSegmentBytes * 2, southbound.MaxLogSegmentBytes * 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := newNodeRateLimiter(tc.rate)
			if l.burst != float64(tc.wantBurst) {
				t.Fatalf("rate=%d: got burst %v, want %v", tc.rate, l.burst, tc.wantBurst)
			}
		})
	}
}

// TestNodeRateLimiterAdmitsAfterEnoughWaitButNotBefore is the regression test for the bug this fix addresses:
// before it, a nodeRateLimiter's burst was capped at its steady-state rate, so a single honest request larger
// than rate but no larger than southbound.MaxLogSegmentBytes (an outlier segment approaching that size, see its
// doc comment) could never be admitted, no matter how long the caller waited — allow() always returned false for
// it, permanently 429ing that node's upload. Table-driven across n < rate, n == rate, and rate < n <=
// MaxLogSegmentBytes: every case must be admitted immediately from a fresh bucket, must be rejected (429) with
// no elapsed time right after a bucket is drained, and must be admitted again once enough wall time has passed
// to refill it at rate bytes/second.
func TestNodeRateLimiterAdmitsAfterEnoughWaitButNotBefore(t *testing.T) {
	const rate = 4 << 20 // 4MiB/s, well below southbound.MaxLogSegmentBytes so the clamp actually matters here
	tests := []struct {
		name string
		n    int64
	}{
		{"n below the rate", rate / 2},
		{"n equal to the rate", rate},
		{"n above the rate but within MaxLogSegmentBytes", southbound.MaxLogSegmentBytes},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// A fresh bucket starts full at the burst floor (>= MaxLogSegmentBytes, see
			// TestNodeRateLimiterBurstFloorsAtMaxLogSegmentBytes), so n is admitted right away regardless of how
			// it compares to the steady-state rate.
			fresh := newNodeRateLimiter(rate)
			if !fresh.allow("node-a", tc.n) {
				t.Fatalf("expected a fresh bucket to admit %d bytes immediately", tc.n)
			}

			// Drain a second, independent bucket completely, then confirm n more bytes is rejected with no
			// elapsed time (429; the honest-node case the original bug turned into a permanent block for n >
			// rate), and admitted once enough wall time has passed to refill it at rate bytes/second.
			drained := newNodeRateLimiter(rate)
			if !drained.allow("node-a", int64(drained.burst)) {
				t.Fatalf("expected the initial burst (%v bytes) to be admitted in one call", drained.burst)
			}
			if drained.allow("node-a", tc.n) {
				t.Fatal("expected a just-drained bucket to reject more bytes with no elapsed time (429)")
			}
			drained.mu.Lock()
			b := drained.buckets["node-a"]
			// Rewind the bucket's clock, rather than sleeping, to simulate the wait needed to refill tc.n bytes
			// at rate bytes/second without slowing this test down for the largest case (32MiB / 4MiB/s = 8s).
			b.updated = b.updated.Add(-time.Duration(float64(tc.n)/float64(rate)*float64(time.Second)) - time.Millisecond)
			drained.mu.Unlock()
			if !drained.allow("node-a", tc.n) {
				t.Fatalf("expected %d bytes to be admitted after waiting long enough to refill them at %d bytes/second", tc.n, int64(rate))
			}
		})
	}
}

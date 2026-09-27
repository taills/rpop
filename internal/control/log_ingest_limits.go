package control

import (
	"sync"
	"time"

	"github.com/rpop-project/rpop/internal/southbound"
)

const (
	// DefaultMaxConcurrentLogIngests bounds how many log segment uploads the controller processes at once,
	// across every node (stage 5 security review item 5). Each one holds up to maxDecompressedLogSegmentBytes
	// (64MiB) of decompressed NDJSON in memory while it decodes and writes it, so with no cap that working set
	// scales linearly with however many nodes happen to upload at the same instant. 8 concurrent ingests bounds
	// it to 512MiB, comfortably within what a modest controller host has to spare, while still letting several
	// nodes upload in parallel instead of queuing behind each other one at a time.
	DefaultMaxConcurrentLogIngests = 8
	// DefaultLogIngestRateBytesPerSecond bounds how fast the controller accepts compressed bytes from a single
	// node (stage 5 security review item 5), as a *steady-state* average; the global concurrency cap above
	// bounds how many nodes may upload at once, this bounds how fast any one of them may over time, independently
	// of the others. It is set to 4x spool.DefaultUploadRateBytesPerSecond (4MiB/s), a node's own default upload
	// throttle, so a node observing its own default configuration is never throttled here over any sustained
	// period; a sustained rate several times that default from a single node is unusual enough (a misconfigured
	// or compromised node, or one an operator forgot to bound) to be worth slowing down rather than treated as
	// normal variance. This is deliberately smaller than southbound.MaxLogSegmentBytes (32MiB): it bounds
	// long-run throughput, not the size of any one request; see nodeRateLimiter's burst for that.
	DefaultLogIngestRateBytesPerSecond int64 = 16 << 20
)

// nodeRateLimiter enforces a per-node upload byte-rate cap on log ingest (stage 5 security review item 5),
// independent of the global concurrency semaphore: that bounds how many nodes may upload at once, this bounds
// how fast any single one may, on average, over time. Tokens refill continuously from elapsed wall time at rate
// bytes/second, mirroring internal/spool's own tokenBucket (the node-side equivalent of this limiter, which
// throttles how fast a node sends rather than how fast the controller accepts).
//
// burst — the bucket's capacity, and what a fresh node's bucket starts full at — is a separate ceiling from
// rate: it bounds how much of the budget a single request may spend at once, not the long-run rate. It is
// always at least southbound.MaxLogSegmentBytes (see newNodeRateLimiter), so a single honest request for an
// outlier segment approaching that size (see its doc comment) is throttled, never rejected outright no matter
// how long the caller waits. An earlier version of this limiter capped burst at rate instead: with the shipped
// default rate (16MiB/s) smaller than southbound.MaxLogSegmentBytes (32MiB), a single legitimate oversized
// segment could never be admitted — allow() always returned false for it, however long the caller waited,
// because tokens never refilled past rate — and since a node's uploader sends segments strictly in order and
// does not advance past one that fails, that permanently wedged its entire upload pipeline, for an honest node
// that never did anything wrong. The node's uploader already retries a non-2xx response, and a rate-limited
// request never advances its high-water mark (see southboundLogs), so throttling one only delays it; nothing
// short-circuits the steady-state limit rate still enforces once a node's burst is exhausted.
type nodeRateLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[string]*rateBucket
}

type rateBucket struct {
	tokens  float64
	updated time.Time
}

// newNodeRateLimiter builds a nodeRateLimiter for production use. burst is clamped up to at least
// southbound.MaxLogSegmentBytes regardless of ratePerSecond (see nodeRateLimiter's doc comment), so a caller —
// including SetLogIngestLimits, which lets an operator override ratePerSecond — can never configure the two
// into the deadlock described there just by choosing a small rate.
func newNodeRateLimiter(ratePerSecond int64) *nodeRateLimiter {
	burst := ratePerSecond
	if burst < southbound.MaxLogSegmentBytes {
		burst = southbound.MaxLogSegmentBytes
	}
	return newNodeRateLimiterWithBurst(ratePerSecond, burst)
}

// newNodeRateLimiterWithBurst is newNodeRateLimiter with burst left unclamped, so tests can exercise the bucket
// algorithm itself (fresh-full, refill, forget) with small, fast numbers instead of ones on the order of
// southbound.MaxLogSegmentBytes. Production code should call newNodeRateLimiter instead.
func newNodeRateLimiterWithBurst(ratePerSecond, burstBytes int64) *nodeRateLimiter {
	return &nodeRateLimiter{rate: float64(ratePerSecond), burst: float64(burstBytes), buckets: make(map[string]*rateBucket)}
}

// allow reports whether nodeID may spend n bytes right now, consuming them from its bucket if so. A node's
// bucket is created full (at l.burst) on its first call, so a node's very first upload is never throttled just
// for having started idle.
func (l *nodeRateLimiter) allow(nodeID string, n int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[nodeID]
	if !ok {
		b = &rateBucket{tokens: l.burst, updated: now}
		l.buckets[nodeID] = b
	} else if elapsed := now.Sub(b.updated).Seconds(); elapsed > 0 {
		b.tokens = min(b.tokens+elapsed*l.rate, l.burst)
		b.updated = now
	}
	if b.tokens < float64(n) {
		return false
	}
	b.tokens -= float64(n)
	return true
}

// forget drops a node's bucket once its node is deleted, mirroring keyedMutex.forget for the same reason: a
// long-lived controller should not keep one bucket per node ID that ever existed.
func (l *nodeRateLimiter) forget(nodeID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, nodeID)
}

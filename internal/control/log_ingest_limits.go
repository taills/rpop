package control

import (
	"sync"
	"time"
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
	// node (stage 5 security review item 5); the global concurrency cap above bounds how many nodes may upload
	// at once, this bounds how fast any one of them may, independently of the others. It is set to 4x
	// spool.DefaultUploadRateBytesPerSecond (4MiB/s), a node's own default upload throttle, so a node observing
	// its own default configuration is never throttled here; a sustained rate several times that default from a
	// single node is unusual enough (a misconfigured or compromised node, or one an operator forgot to bound) to
	// be worth slowing down rather than treated as normal variance.
	DefaultLogIngestRateBytesPerSecond int64 = 16 << 20
)

// nodeRateLimiter enforces a per-node upload byte-rate cap on log ingest (stage 5 security review item 5),
// independent of the global concurrency semaphore: that bounds how many nodes may upload at once, this bounds
// how fast any single one may. Tokens refill continuously from elapsed wall time, mirroring internal/spool's
// own tokenBucket (the node-side equivalent of this limiter, which throttles how fast a node sends rather than
// how fast the controller accepts); burst equals one second's worth of the configured rate, the same choice
// spool's limiter makes. A single request larger than the burst (an outlier segment approaching
// southbound.MaxLogSegmentBytes, see its doc comment) is throttled rather than rejected outright: the node's
// uploader already retries a non-2xx response, and a rate-limited request never advances its high-water mark
// (see southboundLogs), so nothing is lost, only delayed.
type nodeRateLimiter struct {
	mu      sync.Mutex
	rate    float64
	buckets map[string]*rateBucket
}

type rateBucket struct {
	tokens  float64
	updated time.Time
}

func newNodeRateLimiter(ratePerSecond int64) *nodeRateLimiter {
	return &nodeRateLimiter{rate: float64(ratePerSecond), buckets: make(map[string]*rateBucket)}
}

// allow reports whether nodeID may spend n bytes right now, consuming them from its bucket if so. A node's
// bucket is created full on its first call, so a node's very first upload is never throttled just for having
// started idle.
func (l *nodeRateLimiter) allow(nodeID string, n int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[nodeID]
	if !ok {
		b = &rateBucket{tokens: l.rate, updated: now}
		l.buckets[nodeID] = b
	} else if elapsed := now.Sub(b.updated).Seconds(); elapsed > 0 {
		b.tokens = min(b.tokens+elapsed*l.rate, l.rate)
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

package dataplane

import (
	"context"
	"sync"
	"time"

	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/snapshot"
)

// pathProbe drives D19 active probing for one candidate path: once the path enters cooldown, it fires a single
// dial attempt at the instant the cooldown would otherwise expire, so a path that has recovered goes back into
// service immediately instead of waiting for the next real request to discover that. It reuses the exact dial
// code a real request would use (dialer.DialPath, the same call newPathTransports' DialContext makes, D1) but
// closes the connection the moment it is established, without sending any request bytes. Success calls the
// pathTransport's own succeeded(); failure calls its own failed(), which reschedules the cooldown (and, through
// it, this probe) exactly as a failed real request would.
//
// A pathProbe only ever has a pending timer while its path is cooling down, so the cost of D19 is proportional
// to the number of currently-cooling paths — a healthy path carries no timer and no goroutine.
type pathProbe struct {
	path            snapshot.Path
	dialer          PathDialer
	logTunnelEvents bool
	dialTimeout     time.Duration
	target          *pathTransport

	mu         sync.Mutex
	timer      *time.Timer
	dialCancel context.CancelFunc // cancels an in-flight probe dial, if any; nil otherwise
	closed     bool
}

func newPathProbe(path snapshot.Path, dialer PathDialer, logTunnelEvents bool, dialTimeout time.Duration, target *pathTransport) *pathProbe {
	return &pathProbe{path: path, dialer: dialer, logTunnelEvents: logTunnelEvents, dialTimeout: dialTimeout, target: target}
}

// scheduleAt arms a one-shot timer that probes the path at until; a timer already pending is replaced, so at
// most one probe is ever scheduled per path. A no-op once close has been called.
func (pr *pathProbe) scheduleAt(until time.Time) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if pr.closed {
		return
	}
	if pr.timer != nil {
		pr.timer.Stop()
	}
	delay := time.Until(until)
	if delay < 0 {
		delay = 0
	}
	pr.timer = time.AfterFunc(delay, pr.fire)
}

// cancel stops a pending probe, e.g. because a real request already found the path healthy again.
func (pr *pathProbe) cancel() {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if pr.timer != nil {
		pr.timer.Stop()
		pr.timer = nil
	}
}

// close stops a pending probe, cancels an in-flight one, and prevents scheduling new ones. Call exactly once,
// when the runtime that owns this path is released, so this probe's timer and goroutine never outlive it.
func (pr *pathProbe) close() {
	pr.mu.Lock()
	pr.closed = true
	if pr.timer != nil {
		pr.timer.Stop()
		pr.timer = nil
	}
	cancel := pr.dialCancel
	pr.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// fire runs one probe attempt. It is only ever invoked by pr.timer, in its own goroutine.
func (pr *pathProbe) fire() {
	pr.mu.Lock()
	if pr.closed {
		pr.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), pr.dialTimeout)
	pr.dialCancel = cancel
	pr.mu.Unlock()
	defer cancel()

	// Same dial path a real request takes (see newPathTransports' DialContext): the site's own access-log
	// setting travels through the context, and overlay reads it back on the other side of a tunnel.
	conn, err := pr.dialer.DialPath(overlay.WithTunnelLogging(ctx, pr.logTunnelEvents), pr.path)

	pr.mu.Lock()
	pr.dialCancel = nil
	closed := pr.closed
	pr.mu.Unlock()
	if closed {
		// close() ran while the dial was in flight; its cancel already unblocked it, so just release the result.
		if conn != nil {
			_ = conn.Close()
		}
		return
	}
	if err != nil {
		pr.target.failed(time.Now(), err)
		return
	}
	_ = conn.Close()
	pr.target.succeeded()
}

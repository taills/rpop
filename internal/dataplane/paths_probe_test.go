package dataplane

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rpop-project/rpop/internal/snapshot"
)

func boolPtr(b bool) *bool { return &b }

// toggleDialer dials the real network once dial-time fail is false and fails otherwise, counting attempts by
// path label; unlike fakePaths (which classifies by label prefix once and for all), a test can flip fail while
// the engine is running, to simulate a path that recovers on its own between requests.
type toggleDialer struct {
	mu       sync.Mutex
	fail     bool
	attempts map[string]int
}

func (d *toggleDialer) DialPath(ctx context.Context, path snapshot.Path) (net.Conn, error) {
	d.mu.Lock()
	if d.attempts == nil {
		d.attempts = make(map[string]int)
	}
	d.attempts[path.Label]++
	fail := d.fail
	d.mu.Unlock()
	if fail {
		return nil, errors.New("path is down")
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", path.Target)
}

func (d *toggleDialer) setFail(fail bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fail = fail
}

func (d *toggleDialer) count(label string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.attempts[label]
}

func TestResolvePathFailoverConfig(t *testing.T) {
	for _, tc := range []struct {
		name     string
		override *snapshot.UpstreamFailover
		global   bool
		want     pathFailoverConfig
	}{
		{"nil override keeps every default, global enabled", nil, true,
			pathFailoverConfig{dialTimeout: PathEstablishTimeout, minCooldown: MinPathCooldown, maxCooldown: MaxPathCooldown, activeProbe: true}},
		{"nil override keeps every default, global disabled", nil, false,
			pathFailoverConfig{dialTimeout: PathEstablishTimeout, minCooldown: MinPathCooldown, maxCooldown: MaxPathCooldown, activeProbe: false}},
		{"a set field overrides, the rest keep their default", &snapshot.UpstreamFailover{DialTimeoutMs: 5000}, true,
			pathFailoverConfig{dialTimeout: 5 * time.Second, minCooldown: MinPathCooldown, maxCooldown: MaxPathCooldown, activeProbe: true}},
		{"every field overridden", &snapshot.UpstreamFailover{DialTimeoutMs: 2000, MinCooldownMs: 500, MaxCooldownMs: 30000, ActiveProbe: boolPtr(false)}, true,
			pathFailoverConfig{dialTimeout: 2 * time.Second, minCooldown: 500 * time.Millisecond, maxCooldown: 30 * time.Second, activeProbe: false}},
		{"activeProbe:true overrides a disabled global default", &snapshot.UpstreamFailover{ActiveProbe: boolPtr(true)}, false,
			pathFailoverConfig{dialTimeout: PathEstablishTimeout, minCooldown: MinPathCooldown, maxCooldown: MaxPathCooldown, activeProbe: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolvePathFailoverConfig(tc.override, tc.global); got != tc.want {
				t.Fatalf("resolvePathFailoverConfig(%#v, %v) = %#v, want %#v", tc.override, tc.global, got, tc.want)
			}
		})
	}
}

// probeSite applies a single-path site whose upstream carries failover, so the D19 test suite always exercises
// the same, fast cooldown regardless of the node-wide defaults (1s-1min would make every test slow).
func probeSite(t *testing.T, engine *Engine, upstream string, target, label string, failover *snapshot.UpstreamFailover) int {
	t.Helper()
	port := freePort(t)
	site := snapshot.Site{ID: "probe", ListenAddress: "127.0.0.1", ListenPort: port, Upstreams: []snapshot.Upstream{{
		URL: upstream, Paths: []snapshot.Path{{Label: label, Target: target}}, Failover: failover,
	}}}
	mustApply(t, engine, site)
	return port
}

func fastFailover() *snapshot.UpstreamFailover {
	return &snapshot.UpstreamFailover{MinCooldownMs: 20, MaxCooldownMs: 20}
}

// awaitPathHealth polls PathHealth (never sending another request) until predicate is satisfied or timeout.
func awaitPathHealth(t *testing.T, engine *Engine, timeout time.Duration, predicate func(PathHealth) bool) PathHealth {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last PathHealth
	for time.Now().Before(deadline) {
		health := engine.PathHealth()
		if len(health) == 1 && len(health[0].Paths) == 1 {
			last = health[0].Paths[0]
			if predicate(last) {
				return last
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("path health never matched: last = %#v", last)
	return PathHealth{}
}

func TestActiveProbeRestoresAPathAsSoonAsItsCooldownExpires(t *testing.T) {
	engine := newTestEngine(t)
	dialer := &toggleDialer{fail: true}
	engine.SetPathDialer(dialer)
	upstream := textUpstream(t, "ok")
	target := strings.TrimPrefix(upstream.URL, "http://")
	port := probeSite(t, engine, upstream.URL, target, "flaky", fastFailover())
	get(t, port, "") // the only real request: fails, starting the path's cooldown

	awaitPathHealth(t, engine, time.Second, func(h PathHealth) bool { return h.Status == "cooling" })
	dialer.setFail(false) // the path "recovers" on its own; only the probe, not a client, will ever notice

	// Status flips to "healthy" the instant p.until passes, purely from comparing it to the current time (see
	// pathTransport.health) - the same instant the probe's timer fires, before its dial has even started. So a
	// poll can catch a brief, legitimate window where Status already reads "healthy" but Failures/LastError
	// still hold the prior failure, because only the probe's own success (recordSuccessLocked) clears them.
	// Waiting for the full expected state, rather than Status alone, still fails (via awaitPathHealth's
	// deadline) if the probe's success never lands, so it does not hide a real regression.
	awaitPathHealth(t, engine, 2*time.Second, func(h PathHealth) bool {
		return h.Status == "healthy" && h.Failures == 0 && h.LastError == ""
	})
}

func TestActiveProbeReschedulesTheCooldownWhenItFailsToo(t *testing.T) {
	engine := newTestEngine(t)
	dialer := &toggleDialer{fail: true}
	engine.SetPathDialer(dialer)
	upstream := textUpstream(t, "ok")
	target := strings.TrimPrefix(upstream.URL, "http://")
	port := probeSite(t, engine, upstream.URL, target, "down", &snapshot.UpstreamFailover{MinCooldownMs: 20, MaxCooldownMs: 20000})
	get(t, port, "")

	// Without a second real request, only a probe that fired, failed, and re-armed itself can grow Failures.
	health := awaitPathHealth(t, engine, time.Second, func(h PathHealth) bool { return h.Failures >= 2 })
	if health.Status != "cooling" {
		t.Fatalf("path health after two failures = %#v", health)
	}
}

func TestActiveProbeCanBeDisabledPerUpstream(t *testing.T) {
	engine := newTestEngine(t)
	dialer := &toggleDialer{fail: true}
	engine.SetPathDialer(dialer)
	upstream := textUpstream(t, "ok")
	target := strings.TrimPrefix(upstream.URL, "http://")
	failover := fastFailover()
	failover.ActiveProbe = boolPtr(false)
	port := probeSite(t, engine, upstream.URL, target, "flaky", failover)
	get(t, port, "")

	attempts := dialer.count("flaky")
	time.Sleep(300 * time.Millisecond) // far longer than the 20ms cooldown a probe would have fired within
	if got := dialer.count("flaky"); got != attempts {
		t.Fatalf("upstreams[0].failover.activeProbe=false did not stop probing: attempts %d -> %d", attempts, got)
	}
}

// TestSetPathActiveProbeDisablesProbingGlobally covers the engine-wide default (D30): an upstream with no
// Failover.ActiveProbe of its own inherits it.
func TestSetPathActiveProbeDisablesProbingGlobally(t *testing.T) {
	engine := newTestEngine(t)
	engine.SetPathActiveProbe(false)
	dialer := &toggleDialer{fail: true}
	engine.SetPathDialer(dialer)
	upstream := textUpstream(t, "ok")
	target := strings.TrimPrefix(upstream.URL, "http://")
	port := probeSite(t, engine, upstream.URL, target, "flaky", fastFailover())
	get(t, port, "")

	attempts := dialer.count("flaky")
	time.Sleep(300 * time.Millisecond)
	if got := dialer.count("flaky"); got != attempts {
		t.Fatalf("SetPathActiveProbe(false) did not stop probing: attempts %d -> %d", attempts, got)
	}
}

// TestHandlerBuiltRuntimeNeverActivelyProbes covers the one exception to the global default: a runtime built by
// Engine.Handler (used for one-off previews, see proxyHandler) is never released, so nothing would ever stop a
// probe timer scheduled on it; see Engine.Handler's doc comment.
func TestHandlerBuiltRuntimeNeverActivelyProbes(t *testing.T) {
	engine := newTestEngine(t)
	dialer := &toggleDialer{fail: true}
	engine.SetPathDialer(dialer)
	upstream := textUpstream(t, "ok")
	site := pathSite(t, engine, upstream, "flaky")
	if _, err := http.Get(site.URL); err != nil {
		t.Fatal(err)
	}

	attempts := dialer.count("flaky")
	time.Sleep(1200 * time.Millisecond) // longer than the 1s default minimum cooldown pathSite's upstream uses
	if got := dialer.count("flaky"); got != attempts {
		t.Fatalf("a Handler()-built runtime actively probed (attempts %d -> %d); it has no owner to release the timer", attempts, got)
	}
}

// TestActiveProbeStopsWhenTheSiteIsStopped is the D19 resource-cleanup contract: once a probe is scheduled, no
// timer or goroutine may outlive the runtime that owns it. Stopping the engine immediately after the failure
// that schedules a probe, then waiting far longer than several cooldown cycles would have taken, is a stronger
// (and less flaky) proof of no leak than counting goroutines: any leaked timer would keep dialing forever.
func TestActiveProbeStopsWhenTheSiteIsStopped(t *testing.T) {
	engine := newTestEngine(t)
	dialer := &toggleDialer{fail: true}
	engine.SetPathDialer(dialer)
	upstream := textUpstream(t, "ok")
	target := strings.TrimPrefix(upstream.URL, "http://")
	port := probeSite(t, engine, upstream.URL, target, "flaky", fastFailover())
	get(t, port, "") // fails and schedules a probe ~20ms out

	engine.Stop("probe") // release the runtime before the probe timer fires
	attempts := dialer.count("flaky")
	time.Sleep(300 * time.Millisecond) // long enough for several 20ms cooldown cycles, had the timer survived
	if got := dialer.count("flaky"); got != attempts {
		t.Fatalf("dial attempts grew from %d to %d after Stop; the D19 probe timer leaked", attempts, got)
	}
}

// TestActiveProbeStopsWhenTheSnapshotRebuildsTheSite is TestActiveProbeStopsWhenTheSiteIsStopped's counterpart
// for the other way a runtime is released: Apply replacing it with a new one (e.g. its paths changed), rather
// than the site being stopped outright.
func TestActiveProbeStopsWhenTheSnapshotRebuildsTheSite(t *testing.T) {
	engine := newTestEngine(t)
	dialer := &toggleDialer{fail: true}
	engine.SetPathDialer(dialer)
	upstream := textUpstream(t, "ok")
	target := strings.TrimPrefix(upstream.URL, "http://")
	port := probeSite(t, engine, upstream.URL, target, "flaky", fastFailover())
	get(t, port, "")

	// A different path label changes the site's RuntimeKey, so Apply rebuilds the runtime and releases the old
	// one instead of only swapping the certificate in place.
	site := snapshot.Site{ID: "probe", ListenAddress: "127.0.0.1", ListenPort: port, Upstreams: []snapshot.Upstream{{
		URL: upstream.URL, Paths: []snapshot.Path{{Label: "flaky-2", Target: target}}, Failover: fastFailover(),
	}}}
	mustApply(t, engine, site)

	attempts := dialer.count("flaky")
	time.Sleep(300 * time.Millisecond)
	if got := dialer.count("flaky"); got != attempts {
		t.Fatalf("dial attempts on the replaced path grew from %d to %d after the site was rebuilt; the D19 probe timer leaked", attempts, got)
	}
}

// pauseDialer blocks its pauseAt'th DialPath call on release (closing started right before it blocks), so a
// test can inject a concurrent event at a precise moment instead of guessing with a sleep. Calls other than
// pauseAt never block.
type pauseDialer struct {
	mu      sync.Mutex
	fail    bool
	calls   int
	pauseAt int
	started chan struct{}
	release chan struct{}
}

func (d *pauseDialer) DialPath(ctx context.Context, path snapshot.Path) (net.Conn, error) {
	d.mu.Lock()
	d.calls++
	pause := d.calls == d.pauseAt
	d.mu.Unlock()
	if pause {
		close(d.started)
		<-d.release
	}
	d.mu.Lock()
	fail := d.fail
	d.mu.Unlock()
	if fail {
		return nil, errors.New("path is down")
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", path.Target)
}

func (d *pauseDialer) setFail(fail bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fail = fail
}

// pathTransportFor reaches into the engine's internal state to find the exact *pathTransport a concurrent real
// request would call failed()/succeeded() on, for a white-box race test.
func pathTransportFor(t *testing.T, engine *Engine, siteID, label string) *pathTransport {
	t.Helper()
	engine.mu.Lock()
	run := engine.runs[siteID]
	engine.mu.Unlock()
	if run == nil {
		t.Fatalf("no running site %q", siteID)
	}
	for _, group := range run.route.pathGroups {
		for _, p := range group.failover.paths {
			if p.label == label {
				return p
			}
		}
	}
	t.Fatalf("no path %q on site %q", label, siteID)
	return nil
}

// TestActiveProbeRaceDoesNotUndoAConcurrentFailure covers the fix for the cooldownEpoch race: a probe dial
// that races with a real request's own failed() call must never let its own (by then stale) result overwrite
// the real request's more recent outcome, nor cancel the retry that outcome scheduled.
func TestActiveProbeRaceDoesNotUndoAConcurrentFailure(t *testing.T) {
	engine := newTestEngine(t)
	dialer := &pauseDialer{fail: true, pauseAt: 2, started: make(chan struct{}), release: make(chan struct{})}
	engine.SetPathDialer(dialer)
	upstream := textUpstream(t, "ok")
	target := strings.TrimPrefix(upstream.URL, "http://")
	// A long, fixed cooldown gives a wide window to observe the race's outcome before the concurrent failure's
	// own, legitimate probe would restore the path on its own.
	port := probeSite(t, engine, upstream.URL, target, "flaky", &snapshot.UpstreamFailover{MinCooldownMs: 200, MaxCooldownMs: 200})
	get(t, port, "") // real request #1 (dial call #1) fails, starting the cooldown and scheduling a probe

	select {
	case <-dialer.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the probe's dial (call #2) never started")
	}
	// The probe's dial is now blocked mid-flight, captured at the pre-failure epoch. Inject a concurrent real
	// request's failure directly on the path, the way a second, parallel real request would.
	pt := pathTransportFor(t, engine, "probe", "flaky")
	pt.failed(time.Now(), errors.New("a concurrent real request failed"))

	dialer.setFail(false) // once released, the paused probe dial succeeds - a stale result by now
	close(dialer.release)

	// Give the stale success a moment to (incorrectly, without the epoch guard) apply.
	time.Sleep(150 * time.Millisecond)
	if health := engine.PathHealth()[0].Paths[0]; health.Status != "cooling" {
		t.Fatalf("a stale probe success undid a concurrent real request's failure: %#v", health)
	}

	// The concurrent failure's own probe must still be queued and eventually restore the path.
	awaitPathHealth(t, engine, 2*time.Second, func(h PathHealth) bool { return h.Status == "healthy" })
}

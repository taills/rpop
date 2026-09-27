package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/southbound"
)

// rejectingGate always answers a specific southbound path with a fixed status and body, counting how many
// times it was hit, and forwards every other request to the real handler unchanged. It stands in for a
// persistent, non-transient failure — here, D27's protocol version window rejecting every attempt — to prove a
// node backs off on authRetryInterval instead of retrying on the tight reconnect backoff meant for ordinary
// transient failures (see registerUntilDone/watchLoop's isUpgradeRequired branches).
type rejectingGate struct {
	handler http.Handler
	path    string
	status  int
	body    string
	mu      sync.Mutex
	hits    int
}

func (g *rejectingGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == g.path {
		g.mu.Lock()
		g.hits++
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(g.status)
		_, _ = io.WriteString(w, g.body)
		return
	}
	g.handler.ServeHTTP(w, r)
}

func (g *rejectingGate) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.hits
}

const upgradeRequiredBody = `{"error":"node speaks protocol version 999, this controller supports up to 1; upgrade the controller before the node"}`

// TestRegisterUntilDoneBacksOffLongerAfterAProtocolVersionRejection covers registerUntilDone: a 426 Upgrade
// Required is not a transient failure (retrying sooner cannot help until someone upgrades something), so it
// must retry on authRetryInterval (a minute), not the tight exponential backoff (capped at 30s) transient
// registration failures use. A 3s window can only ever contain the very first attempt at that pace, so the
// count assertion is exact, not a range.
func TestRegisterUntilDoneBacksOffLongerAfterAProtocolVersionRejection(t *testing.T) {
	gate := &rejectingGate{path: southbound.RegisterPath, status: http.StatusUpgradeRequired, body: upgradeRequiredBody}
	c := startLoggingController(t, func(real http.Handler) http.Handler {
		gate.handler = real
		return gate
	})
	token := c.createNode("edge-1")
	node, err := New(Config{ControllerURL: c.southbound.URL, JoinToken: token, DataDir: t.TempDir()}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := node.registerUntilDone(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("registerUntilDone returned %v before its context timed out", err)
	}
	if got := gate.count(); got != 1 {
		t.Fatalf("register was attempted %d times in 3s, want exactly 1 (authRetryInterval is a minute)", got)
	}
}

// TestWatchLoopBacksOffLongerAfterAProtocolVersionRejection covers watchLoop's twin of the above: register
// succeeds normally (the gate only rejects WatchPath), then every watch attempt is rejected, and the same
// authRetryInterval pacing applies.
func TestWatchLoopBacksOffLongerAfterAProtocolVersionRejection(t *testing.T) {
	gate := &rejectingGate{path: southbound.WatchPath, status: http.StatusUpgradeRequired, body: upgradeRequiredBody}
	c := startLoggingController(t, func(real http.Handler) http.Handler {
		gate.handler = real
		return gate
	})
	token := c.createNode("edge-1")
	base, err := url.Parse(c.southbound.URL)
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	identity, err := register(context.Background(), base, dataDir, token)
	if err != nil {
		t.Fatal(err)
	}
	node, err := New(Config{ControllerURL: c.southbound.URL, DataDir: dataDir}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	node.use(identity)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	node.watchLoop(ctx)
	if got := gate.count(); got != 1 {
		t.Fatalf("watch was attempted %d times in 3s, want exactly 1 (authRetryInterval is a minute)", got)
	}
}

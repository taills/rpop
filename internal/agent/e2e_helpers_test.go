package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/accesslog"
	"github.com/rpop-project/rpop/internal/control"
	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/southbound"
	"github.com/rpop-project/rpop/internal/store"
)

// This file holds shared test infrastructure for the stage 5 (logging/tracing) end-to-end tests in this
// package: a real node (*Agent) registered against a real controller (*control.Control), talking real mTLS,
// spooling to a real temp-dir spool and uploading it over a real (if faked-out) southbound HTTP/2 listener. It
// builds on startController/startAgent/controller from agent_test.go and overlay_test.go rather than
// duplicating them; it only adds what those did not need: a controller with real log storage (NewWithLogDir)
// and a couple of southbound-handler wrappers that simulate a controller outage or a lost acknowledgment.

// newLoggingControlService builds a *control.Control the way NewWithLogDir does: with a real file access log
// adapter and tunnel event store backed by a temp directory. startController (agent_test.go) uses plain
// control.New, which leaves both nil - fine for registration/watch/status tests, but the southbound logs
// endpoint (D23) and the /api/logging/* queries this file's tests exercise need the real thing.
func newLoggingControlService(t *testing.T) *control.Control {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	service, err := control.NewWithLogDir(store.New(db), zap.NewNop(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service.SetEmbeddedNode(false)
	t.Cleanup(func() {
		service.StopAll()
		_ = service.Close(context.Background())
	})
	return service
}

// startLoggingController is startController's twin for this file's tests: it wraps the real southbound handler
// with wrap (pass nil for no wrapping) before serving it, and returns the same *controller type agent_test.go
// and overlay_test.go define, so every helper written for it (call, createNode, createRelayNode, node, inSync,
// ...) keeps working unchanged.
func startLoggingController(t *testing.T, wrap func(http.Handler) http.Handler) *controller {
	t.Helper()
	service := newLoggingControlService(t)
	handler := service.SouthboundHandler()
	if wrap != nil {
		handler = wrap(handler)
	}
	tlsConfig, err := service.SouthboundTLSConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS, server.EnableHTTP2 = tlsConfig, true
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	c := &controller{t: t, service: service, console: service.Handler(), southbound: server}
	setup := c.call(http.MethodPost, "/api/auth/setup", `{"password":"test-admin-password-2026"}`, http.StatusOK)
	c.cookie = setup.Result().Cookies()[0]
	return c
}

// callRaw is like (*controller).call (agent_test.go) but returns whatever status the API answered instead of
// failing the test, for callers that poll an endpoint while its answer is still converging (e.g. a trace lookup
// before the node has uploaded the segment it needs).
func (c *controller) callRaw(method, path, body string) *httptest.ResponseRecorder {
	c.t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if c.cookie != nil {
		request.AddCookie(c.cookie)
	}
	response := httptest.NewRecorder()
	c.console.ServeHTTP(response, request)
	return response
}

// createSite creates a site placed on nodeID, proxying to upstreamURL, with access logging on adapterID, and
// starts it - the shape every test in this file needs (agent_test.go's own tests build this JSON inline since
// each varies it slightly; this file's tests do not, so one helper covers all of them).
func (c *controller) createSite(id, nodeID, adapterID, upstreamURL string, port int) {
	c.t.Helper()
	body := fmt.Sprintf(`{"id":%q,"name":%q,"config":{"nodes":[%q],"listenAddress":"127.0.0.1","listenPort":%d,"upstreams":[{"url":%q}],"accessLog":{"adapterId":%q}}}`,
		id, id, nodeID, port, upstreamURL, adapterID)
	c.call(http.MethodPost, "/api/sites", body, http.StatusCreated)
	c.call(http.MethodPost, "/api/sites/"+id+"/start", "", http.StatusOK)
}

// reissueToken asks the console API for a fresh single-use join token on an already-registered node, so a test
// can simulate re-registration (a lost/reinstalled node coming back, D26) by starting a new agent with it.
func (c *controller) reissueToken(id string) string {
	c.t.Helper()
	response := c.call(http.MethodPost, "/api/nodes/"+id+"/token", "", http.StatusOK)
	var created struct {
		JoinToken string `json:"joinToken"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.JoinToken == "" {
		c.t.Fatalf("reissue token response %s: %v", response.Body.String(), err)
	}
	return created.JoinToken
}

// nodeLogState is nodeState's (agent_test.go) sibling for reading the Logs field GET /api/nodes exposes
// (internal/control/nodes.go's nodeView.Logs) - the node's self-reported spool/upload health (D23/D24/D25).
type nodeLogState struct {
	ID   string               `json:"id"`
	Logs *southbound.LogStats `json:"logs"`
}

// nodeLogs returns the LogStats the controller last received from id's status reports, or nil if it has not
// reported any yet (e.g. it has never reported a status since it started, or every one so far failed to reach
// a gated-off controller).
func (c *controller) nodeLogs(id string) *southbound.LogStats {
	c.t.Helper()
	var nodes []nodeLogState
	if err := json.Unmarshal(c.call(http.MethodGet, "/api/nodes", "", http.StatusOK).Body.Bytes(), &nodes); err != nil {
		c.t.Fatal(err)
	}
	for _, n := range nodes {
		if n.ID == id {
			return n.Logs
		}
	}
	c.t.Fatalf("node %q is not listed", id)
	return nil
}

// searchSite runs GET /api/logs for siteID's access log records, the console API's own search endpoint, so
// tests assert on exactly what an operator would see.
func (c *controller) searchSite(siteID string) accesslog.SearchResult {
	c.t.Helper()
	var result accesslog.SearchResult
	response := c.call(http.MethodGet, "/api/logs?siteId="+siteID+"&pageSize=100", "", http.StatusOK)
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		c.t.Fatal(err)
	}
	return result
}

// trace calls GET /api/logging/trace/{trackID} and returns the decoded record together with the response
// status, for callers that need to poll until it turns 200 rather than fail immediately on 404.
func (c *controller) trace(trackID string) (accesslog.Record, int) {
	c.t.Helper()
	response := c.callRaw(http.MethodGet, "/api/logging/trace/"+trackID, "")
	var record accesslog.Record
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &record); err != nil {
			c.t.Fatal(err)
		}
	}
	return record, response.Code
}

// tunnelEvents calls GET /api/logging/tunnels/{tunnelID} and decodes its timeline.
func (c *controller) tunnelEvents(tunnelID string) []overlay.TunnelEvent {
	c.t.Helper()
	response := c.call(http.MethodGet, "/api/logging/tunnels/"+tunnelID, "", http.StatusOK)
	var events []overlay.TunnelEvent
	if err := json.Unmarshal(response.Body.Bytes(), &events); err != nil {
		c.t.Fatal(err)
	}
	return events
}

// flush force-seals whatever a node's spool has queued right now (even a segment well under the size/age
// thresholds that normally close one), so the uploader picks it up immediately. Tests call this in a poll loop
// rather than once: access logging is asynchronous (P8 - a bounded queue decouples it from the request path), so
// a record is not guaranteed to have reached the spool the instant an HTTP response finishes.
func flush(node *Agent) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = node.spool.Flush(ctx)
}

// waitForAck polls, flushing node's spool each time, until its uploader has an acknowledged segment number at
// or above want.
func waitForAck(t *testing.T, node *Agent, want uint64) {
	t.Helper()
	eventuallyWithin(t, 20*time.Second, fmt.Sprintf("the node's spool reaches acked segment %d", want), func() bool {
		flush(node)
		return node.spool.Stats().AckedSegment >= want
	})
}

// southboundOutageGate stands in for the controller's southbound listener being completely unreachable: a real
// network partition looks the same to a node (every request fails, nothing is written or acknowledged), so this
// wraps the real handler and answers 503 to everything while closed, without tearing down and rebuilding the
// httptest.Server (and so without changing the address the node already dialed).
type southboundOutageGate struct {
	handler http.Handler
	open    atomic.Bool
}

func newSouthboundOutageGate(handler http.Handler) *southboundOutageGate {
	g := &southboundOutageGate{handler: handler}
	g.open.Store(true)
	return g
}

func (g *southboundOutageGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !g.open.Load() {
		http.Error(w, "simulated controller outage", http.StatusServiceUnavailable)
		return
	}
	g.handler.ServeHTTP(w, r)
}

func (g *southboundOutageGate) close()  { g.open.Store(false) }
func (g *southboundOutageGate) reopen() { g.open.Store(true) }

// ackDropGate lets a specific log segment's next upload attempt run for real (the record gets written and the
// node's high-water mark gets persisted, exactly as a normal upload would) but answers the node with a failure
// instead of the real acknowledgment - standing in for the ack getting lost on the way back after the controller
// already committed the write. D24 is built to make that safe: the write is durable and the HWM is persisted
// before the ACK is ever sent, so the node's retry of the same segment must be answered without writing again.
type ackDropGate struct {
	handler http.Handler
	mu      sync.Mutex
	drop    map[uint64]bool
}

func newAckDropGate(handler http.Handler) *ackDropGate {
	return &ackDropGate{handler: handler, drop: make(map[uint64]bool)}
}

// dropNextAckFor arranges for the next POST of segment seq to be fully processed by the real handler but
// answered with a failure; it only fires once per call (a genuine retransmission of the same segment number
// afterwards gets the real answer).
func (g *ackDropGate) dropNextAckFor(seq uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.drop[seq] = true
}

func (g *ackDropGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != southbound.LogsPath || r.Method != http.MethodPost {
		g.handler.ServeHTTP(w, r)
		return
	}
	seq, _ := strconv.ParseUint(r.Header.Get(southbound.LogSegmentHeader), 10, 64)
	g.mu.Lock()
	drop := g.drop[seq]
	if drop {
		delete(g.drop, seq)
	}
	g.mu.Unlock()
	if !drop {
		g.handler.ServeHTTP(w, r)
		return
	}
	// Run the real handler against a recorder so its write/persist side effects are genuine, then answer the
	// node with a failure instead of relaying whatever the recorder captured.
	g.handler.ServeHTTP(httptest.NewRecorder(), r)
	http.Error(w, "simulated ack loss", http.StatusServiceUnavailable)
}

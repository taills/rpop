package dataplane

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/rpop-project/rpop/internal/accesslog"
	"github.com/rpop-project/rpop/internal/snapshot"
)

var trackIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// fakeAccessLogWriter records every access log record it receives, for assertions.
type fakeAccessLogWriter struct {
	mu      sync.Mutex
	records []accesslog.Record
}

func (w *fakeAccessLogWriter) WriteAccessLog(_ context.Context, _ string, record accesslog.Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.records = append(w.records, record)
	return nil
}

func (w *fakeAccessLogWriter) last() accesslog.Record {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.records[len(w.records)-1]
}

func TestObserveSiteAlwaysMintsItsOwnTrackID(t *testing.T) {
	engine := newTestEngine(t)
	writer := &fakeAccessLogWriter{}
	engine.SetAccessLogWriter(writer)
	var receivedHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get(TrackIDHeader)
	}))
	defer upstream.Close()
	site := snapshot.Site{ID: "s", Upstreams: []snapshot.Upstream{{URL: upstream.URL}}, AccessLog: snapshot.AccessLog{AdapterID: "default"}}
	handler, err := engine.Handler(site)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(TrackIDHeader, "forged-by-client")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if err := engine.DrainAccessLogs(context.Background()); err != nil {
		t.Fatal(err)
	}

	if receivedHeader == "forged-by-client" || !trackIDPattern.MatchString(receivedHeader) {
		t.Fatalf("upstream received track id %q, want a freshly minted UUIDv7 that ignores the client value", receivedHeader)
	}
	if record := writer.last(); record.TrackID != receivedHeader {
		t.Fatalf("access log track id %q does not match what was sent upstream %q", record.TrackID, receivedHeader)
	}
}

func TestObserveSiteMintsTrackIDEvenWithoutAccessLogging(t *testing.T) {
	engine := newTestEngine(t)
	var receivedHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get(TrackIDHeader)
	}))
	defer upstream.Close()
	site := snapshot.Site{ID: "s", Upstreams: []snapshot.Upstream{{URL: upstream.URL}}}
	handler, err := engine.Handler(site)
	if err != nil {
		t.Fatal(err)
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !trackIDPattern.MatchString(receivedHeader) {
		t.Fatalf("upstream still needs a track id even without access logging, got %q", receivedHeader)
	}
}

// fakeTunnelConn exposes the same TunnelID method overlay's tunnel connections do, without importing overlay.
type fakeTunnelConn struct {
	net.Conn
	id string
}

func (c fakeTunnelConn) TunnelID() string { return c.id }

// tlsLikeConn stands in for *tls.Conn's NetConn escape hatch without a real handshake.
type tlsLikeConn struct {
	net.Conn
	inner net.Conn
}

func (c tlsLikeConn) NetConn() net.Conn { return c.inner }

func TestTunnelIDFromConnUnwrapsTLS(t *testing.T) {
	tunnel := fakeTunnelConn{id: "tunnel-123"}
	if got := tunnelIDFromConn(tunnel); got != "tunnel-123" {
		t.Fatalf("direct tunnel conn: got %q", got)
	}
	if got := tunnelIDFromConn(tlsLikeConn{inner: tunnel}); got != "tunnel-123" {
		t.Fatalf("tls-wrapped tunnel conn: got %q", got)
	}
	if got := tunnelIDFromConn(&net.TCPConn{}); got != "" {
		t.Fatalf("a plain connection should carry no tunnel id, got %q", got)
	}
}

// tunnelPathDialer dials target directly but hands back a connection tagged with tunnelID, standing in for a
// path that crosses nodes over the overlay.
type tunnelPathDialer struct{ target, tunnelID string }

func (d tunnelPathDialer) DialPath(ctx context.Context, _ snapshot.Path) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", d.target)
	if err != nil {
		return nil, err
	}
	return fakeTunnelConn{Conn: conn, id: d.tunnelID}, nil
}

func TestObserveSiteRecordsTheTunnelIDOfTheConnectionItUsed(t *testing.T) {
	engine := newTestEngine(t)
	writer := &fakeAccessLogWriter{}
	engine.SetAccessLogWriter(writer)
	upstream := textUpstream(t, "ok")
	target := strings.TrimPrefix(upstream.URL, "http://")
	engine.SetPathDialer(tunnelPathDialer{target: target, tunnelID: "tunnel-abc"})
	site := snapshot.Site{ID: "s", AccessLog: snapshot.AccessLog{AdapterID: "default"},
		Upstreams: []snapshot.Upstream{{URL: upstream.URL, Paths: []snapshot.Path{{Label: "p", Target: target}}}}}
	handler, err := engine.Handler(site)
	if err != nil {
		t.Fatal(err)
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if err := engine.DrainAccessLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := writer.last().TunnelID; got != "tunnel-abc" {
		t.Fatalf("access log tunnel id = %q, want tunnel-abc", got)
	}
}

func TestObserveSiteLeavesTunnelIDEmptyForDirectConnections(t *testing.T) {
	engine := newTestEngine(t)
	writer := &fakeAccessLogWriter{}
	engine.SetAccessLogWriter(writer)
	upstream := textUpstream(t, "ok")
	site := snapshot.Site{ID: "s", AccessLog: snapshot.AccessLog{AdapterID: "default"}, Upstreams: []snapshot.Upstream{{URL: upstream.URL}}}
	handler, err := engine.Handler(site)
	if err != nil {
		t.Fatal(err)
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if err := engine.DrainAccessLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := writer.last().TunnelID; got != "" {
		t.Fatalf("direct connection should not have a tunnel id, got %q", got)
	}
}

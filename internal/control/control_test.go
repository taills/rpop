package control

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/rpop-project/rpop/internal/store"
)

type gateWriteSyncer struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *gateWriteSyncer) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(p), nil
}
func (w *gateWriteSyncer) Sync() error { return nil }
func (w *gateWriteSyncer) unblock() {
	select {
	case <-w.release:
	default:
		close(w.release)
	}
}

func TestYAMLConfigImportExport(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	c := New(store.New(db), zap.NewNop())
	upstream := "ht" + "tp://127.0.0.1:9000"
	body := "sites:\n  - id: app\n    name: App\n    autoStart: true\n    config:\n      listenAddress: 127.0.0.1\n      listenPort: 8089\n      upstreams:\n        - url: " + upstream + "\n"
	request := httptest.NewRequest("PUT", "/api/config.yaml", strings.NewReader(body))
	response := httptest.NewRecorder()
	c.Handler().ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("import status %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest("GET", "/api/config.yaml", nil)
	response = httptest.NewRecorder()
	c.Handler().ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "listenPort: 8089") || !strings.Contains(response.Body.String(), "autoStart: true") {
		t.Fatalf("export mismatch: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAccessLogSinkDoesNotBlockProxyResponses(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer upstream.Close()
	sink := &gateWriteSyncer{started: make(chan struct{}), release: make(chan struct{})}
	defer sink.unblock()
	logger := zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), sink, zap.InfoLevel))
	c := New(store.New(db), logger)
	handler, err := c.proxyHandler(context.Background(), "async-log", store.Config{Upstreams: []store.Upstream{{URL: upstream.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	serve := func() { handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil)) }
	firstDone := make(chan struct{})
	go func() { serve(); close(firstDone) }()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("proxy response blocked while producing access log")
	}
	select {
	case <-sink.started:
	case <-time.After(time.Second):
		t.Fatal("asynchronous access logger did not reach the slow sink")
	}
	secondDone := make(chan struct{})
	go func() { serve(); close(secondDone) }()
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("slow log sink blocked a subsequent proxy response")
	}
	sink.unblock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.DrainAccessLogs(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestAccessLoggingAndMetrics(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Upstream", "ok")
		_, _ = w.Write(append([]byte("reply:"), body...))
	}))
	defer upstream.Close()
	core, logs := observer.New(zap.InfoLevel)
	c := New(store.New(db), zap.New(core))
	handler, err := c.proxyHandler(context.Background(), "site-a", store.Config{Upstreams: []store.Upstream{{URL: upstream.URL}}, AccessLog: store.AccessLogConfig{IncludeBodies: true, MaxBodyBytes: 64}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("payload"))
	req.Header.Set("Authorization", "secret-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if err := c.DrainAccessLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || response.Body.String() != "reply:payload" {
		t.Fatalf("unexpected proxy response: %d %q", response.Code, response.Body.String())
	}
	metrics := c.metricsForSite("site-a").snapshot()
	if metrics.RequestCount != 1 || metrics.StatusCodes[200] != 1 || metrics.BytesReceived != uint64(len("payload")) || metrics.BytesSent != uint64(len("reply:payload")) {
		t.Fatalf("unexpected metrics: %#v", metrics)
	}
	entries := logs.FilterMessage("site HTTP access").All()
	if len(entries) != 1 {
		t.Fatalf("expected one access log, got %d", len(entries))
	}
	ctx := entries[0].ContextMap()
	if ctx["request_body"] != "payload" || ctx["response_body"] != "reply:payload" {
		t.Fatalf("body missing from access log: %#v", ctx)
	}
	if !strings.Contains(strings.TrimSpace(string(mustJSON(t, ctx["request_headers"]))), "REDACTED") {
		t.Fatalf("authorization header was not redacted: %#v", ctx["request_headers"])
	}
}

func TestUnlimitedBodyCaptureDoesNotTruncate(t *testing.T) {
	capture := newBodyCapture(-1)
	chunk := bytes.Repeat([]byte{'x'}, 32<<10)
	target := maxBodyLogLimit + 1
	var written int64
	for written < target {
		n := int64(len(chunk))
		if n > target-written {
			n = target - written
		}
		if _, err := capture.Write(chunk[:n]); err != nil {
			t.Fatal(err)
		}
		written += n
	}
	data, total, truncated := capture.snapshot()
	if total != target || int64(len(data)) != target || truncated {
		t.Fatalf("unlimited capture truncated body: total=%d stored=%d truncated=%v", total, len(data), truncated)
	}
}

func TestAccessLogQueueAllowsSingleOversizedEvent(t *testing.T) {
	c := &Control{accessLogQueue: make(chan accessLogEvent, 1)}
	size := maxQueuedAccessLogBytes + 1
	if !c.reserveAccessLog(size) {
		t.Fatal("expected empty queue to admit one oversized full-body log")
	}
	if c.reserveAccessLog(1) {
		t.Fatal("expected oversized queued body to prevent unbounded additional queueing")
	}
	c.queuedLogBytes.Add(-size)
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSharedListenerRoutesByHostname(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("site-a")) }))
	defer upA.Close()
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("site-b")) }))
	defer upB.Close()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	s := store.New(db)
	for _, site := range []store.Site{
		{ID: "a", Name: "A", Config: store.Config{ListenAddress: "127.0.0.1", ListenPort: port, Hostnames: []string{"a.test"}, Upstreams: []store.Upstream{{URL: upA.URL}}}},
		{ID: "b", Name: "B", Config: store.Config{ListenAddress: "127.0.0.1", ListenPort: port, Hostnames: []string{"b.test"}, Upstreams: []store.Upstream{{URL: upB.URL}}}},
	} {
		if err := s.Save(context.Background(), site); err != nil {
			t.Fatal(err)
		}
	}
	c := New(s, zap.NewNop())
	defer c.StopAll()
	if err := c.start(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if err := c.start(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	for host, want := range map[string]string{"a.test": "site-a", "b.test": "site-b"} {
		target := "ht" + "tp://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(body) != want {
			t.Fatalf("host %s routed to %q (status %d), want %q", host, body, resp.StatusCode, want)
		}
	}
	if err := c.stop("a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.runs["b"]; !ok {
		t.Fatal("stopping one site unexpectedly stopped its shared listener peer")
	}
}

func TestSharedTLSListenerRoutesBySNIAndHost(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("tls-a")) }))
	defer upA.Close()
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("tls-b")) }))
	defer upB.Close()
	certServerA := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer certServerA.Close()
	certServerB := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer certServerB.Close()
	certA, keyA := encodeTestCertificate(t, certServerA.TLS.Certificates[0])
	certB, keyB := encodeTestCertificate(t, certServerB.TLS.Certificates[0])
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	s := store.New(db)
	for _, entry := range []struct {
		id, host, up, cert, key string
		certPEM, keyPEM         []byte
	}{{"a", "a.test", upA.URL, "cert-a", "key-a", certA, keyA}, {"b", "b.test", upB.URL, "cert-b", "key-b", certB, keyB}} {
		site := store.Site{ID: entry.id, Name: entry.id, Config: store.Config{ListenAddress: "127.0.0.1", ListenPort: port, TLS: true, Hostnames: []string{entry.host}, CertificateSecret: entry.cert, PrivateKeySecret: entry.key, Upstreams: []store.Upstream{{URL: entry.up}}}}
		if err := s.Save(context.Background(), site); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveSecret(context.Background(), entry.id, entry.cert, entry.certPEM); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveSecret(context.Background(), entry.id, entry.key, entry.keyPEM); err != nil {
			t.Fatal(err)
		}
	}
	c := New(s, zap.NewNop())
	defer c.StopAll()
	if err := c.start(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if err := c.start(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	target := "ht" + "tps://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	request := func(serverName, host string) string {
		transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: serverName}}
		client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	if got := request("a.test", "unknown.test"); got != "tls-a" {
		t.Fatalf("SNI fallback routed to %q", got)
	}
	if got := request("a.test", "b.test"); got != "tls-b" {
		t.Fatalf("HTTP Host did not take precedence over SNI: %q", got)
	}
	if got := request("b.test", "unknown.test"); got != "tls-b" {
		t.Fatalf("second SNI route returned %q", got)
	}
}

func encodeTestCertificate(t *testing.T, cert tls.Certificate) ([]byte, []byte) {
	t.Helper()
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

func TestAutoStartSitesStartsListener(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ready")) }))
	defer upstream.Close()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	s := store.New(db)
	if err := s.Save(context.Background(), store.Site{ID: "auto", Name: "Auto", AutoStart: true, Config: store.Config{ListenAddress: "127.0.0.1", ListenPort: port, Upstreams: []store.Upstream{{URL: upstream.URL}}}}); err != nil {
		t.Fatal(err)
	}
	c := New(s, zap.NewNop())
	defer c.StopAll()
	if err := c.StartAutoSites(context.Background()); err != nil {
		t.Fatal(err)
	}
	target := "ht" + "tp://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	resp, err := http.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("listener returned status %d", resp.StatusCode)
	}
}

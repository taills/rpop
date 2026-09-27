package control

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/store"
)

// streamStepTimeout bounds how long one streamed chunk may take to cross the proxy. The upstream only sends the
// next chunk after the client has received the previous one, so a buffering proxy deadlocks and trips it.
const streamStepTimeout = 3 * time.Second

// lockstepUpstream streams count chunks and waits for an acknowledgement on ack before writing the next one.
func lockstepUpstream(t *testing.T, contentType string, count int, ack <-chan struct{}) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		controller := http.NewResponseController(w)
		for i := range count {
			if contentType == "text/event-stream" {
				fmt.Fprintf(w, "event: token\ndata: token-%d\n\n", i)
			} else {
				fmt.Fprintf(w, "{\"token\":%d}\n", i)
			}
			if err := controller.Flush(); err != nil {
				return
			}
			if i == count-1 {
				return
			}
			select {
			case <-ack:
			case <-r.Context().Done():
				return
			case <-time.After(2 * streamStepTimeout):
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}

type streamingSite struct {
	control *Control
	port    int
}

// startStreamingSite runs one site on a real loopback listener with access logging and body capture enabled,
// which is the configuration most likely to interfere with streaming.
func startStreamingSite(t *testing.T, upstreamURL string, withTLS bool) streamingSite {
	t.Helper()
	s, _ := openSystemCATestStore(t)
	c, err := NewWithLogDir(s, zap.NewNop(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseAccessLogs(context.Background()) })
	port := freeLoopbackPort(t)
	site := store.Site{ID: "stream", Name: "stream", Config: store.Config{
		ListenAddress: "127.0.0.1", ListenPort: port, Upstreams: []store.Upstream{{URL: upstreamURL}},
		AccessLog: store.AccessLogConfig{AdapterID: "default", IncludeBodies: true, MaxBodyBytes: 256},
	}}
	if withTLS {
		certServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		certPEM, keyPEM := encodeTestCertificate(t, certServer.TLS.Certificates[0])
		certServer.Close()
		site.Config.TLS, site.Config.CertificateSecret, site.Config.PrivateKeySecret = true, "cert", "key"
		if err := c.store.Save(t.Context(), site); err != nil {
			t.Fatal(err)
		}
		if err := c.store.SaveSecret(t.Context(), site.ID, "cert", certPEM); err != nil {
			t.Fatal(err)
		}
		if err := c.store.SaveSecret(t.Context(), site.ID, "key", keyPEM); err != nil {
			t.Fatal(err)
		}
	} else if err := c.store.Save(t.Context(), site); err != nil {
		t.Fatal(err)
	}
	if err := c.start(context.Background(), site.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.StopAll)
	return streamingSite{control: c, port: port}
}

func (s streamingSite) url(scheme string) string {
	return scheme + "://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(s.port))
}

// readLines delivers body lines as they arrive so each step can be awaited with a timeout.
func readLines(body io.Reader) <-chan string {
	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	return lines
}

func awaitLine(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	deadline := time.After(streamStepTimeout)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("stream ended before %q", want)
			}
			if line == want {
				return
			}
		case <-deadline:
			t.Fatalf("%q was not delivered within %s; the proxy is buffering the stream", want, streamStepTimeout)
		}
	}
}

func TestSSEStreamsEachEventWithoutBuffering(t *testing.T) {
	const events = 5
	ack := make(chan struct{})
	upstream := lockstepUpstream(t, "text/event-stream", events, ack)
	site := startStreamingSite(t, upstream.URL, false)

	response, err := http.Get(site.url("http") + "/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("unexpected SSE response: %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	lines := readLines(response.Body)
	for i := range events {
		awaitLine(t, lines, fmt.Sprintf("data: token-%d", i))
		if i < events-1 {
			ack <- struct{}{}
		}
	}
	if err := site.control.DrainAccessLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestChunkedNDJSONStreamIsNotBuffered(t *testing.T) {
	const chunks = 4
	ack := make(chan struct{})
	upstream := lockstepUpstream(t, "application/x-ndjson", chunks, ack)
	site := startStreamingSite(t, upstream.URL, false)

	response, err := http.Post(site.url("http")+"/api/generate", "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	lines := readLines(response.Body)
	for i := range chunks {
		awaitLine(t, lines, fmt.Sprintf("{\"token\":%d}", i))
		if i < chunks-1 {
			ack <- struct{}{}
		}
	}
}

// websocketEchoUpstream accepts an HTTP/1.1 Upgrade and echoes every line back until the peer closes.
func websocketEchoUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "upgrade required", http.StatusUpgradeRequired)
			return
		}
		conn, buffered, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = buffered.Flush()
		for {
			line, err := buffered.ReadString('\n')
			if err != nil {
				return
			}
			if _, err := buffered.WriteString("echo:" + line); err != nil {
				return
			}
			if err := buffered.Flush(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// exerciseWebSocket upgrades conn through the proxy and checks several echo round trips.
func exerciseWebSocket(t *testing.T, conn net.Conn, host string) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(4 * streamStepTimeout))
	fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", host)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade status=%d, want 101", response.StatusCode)
	}
	for i := range 3 {
		message := fmt.Sprintf("frame-%d\n", i)
		started := time.Now()
		if _, err := io.WriteString(conn, message); err != nil {
			t.Fatal(err)
		}
		reply, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if reply != "echo:"+message {
			t.Fatalf("echo=%q, want %q", reply, "echo:"+message)
		}
		if elapsed := time.Since(started); elapsed > streamStepTimeout {
			t.Fatalf("round trip %d took %s", i, elapsed)
		}
	}
}

func TestWebSocketUpgradeRelaysBothDirections(t *testing.T) {
	site := startStreamingSite(t, websocketEchoUpstream(t).URL, false)
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(site.port)))
	if err != nil {
		t.Fatal(err)
	}
	exerciseWebSocket(t, conn, "stream.test")
	_ = conn.Close()
	deadline := time.Now().Add(streamStepTimeout)
	for site.control.engine.Metrics("stream").StatusCodes[http.StatusSwitchingProtocols] != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("upgrade was not recorded: %#v", site.control.engine.Metrics("stream"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTLSListenerServesHTTP2AndStreamsSSE(t *testing.T) {
	const events = 3
	ack := make(chan struct{})
	upstream := lockstepUpstream(t, "text/event-stream", events, ack)
	site := startStreamingSite(t, upstream.URL, true)

	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ForceAttemptHTTP2: true}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport}).Get(site.url("https") + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 {
		t.Fatalf("TLS site listener negotiated %s, want HTTP/2", response.Proto)
	}
	lines := readLines(response.Body)
	for i := range events {
		awaitLine(t, lines, fmt.Sprintf("data: token-%d", i))
		if i < events-1 {
			ack <- struct{}{}
		}
	}
}

func TestTLSListenerKeepsWebSocketOnHTTP1(t *testing.T) {
	site := startStreamingSite(t, websocketEchoUpstream(t).URL, true)
	// Browsers offer h2 first; without RFC 8441 support advertised they open WebSockets over HTTP/1.1.
	conn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(site.port)), &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	exerciseWebSocket(t, conn, "stream.test")
}

// observedHandler proxies to h through a site built from settings, so tests see the data plane's logs and metrics.
func observedHandler(t *testing.T, c *Control, siteID string, settings store.AccessLogConfig, h http.Handler) http.Handler {
	t.Helper()
	upstream := httptest.NewServer(h)
	t.Cleanup(upstream.Close)
	handler, err := c.proxyHandler(context.Background(), siteID, store.Config{Upstreams: []store.Upstream{{URL: upstream.URL}}, AccessLog: settings})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

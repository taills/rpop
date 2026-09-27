package dataplane

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
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

	"github.com/rpop-project/rpop/internal/snapshot"
)

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	engine := New(zap.NewNop())
	t.Cleanup(engine.StopAll)
	return engine
}

func textUpstream(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
	t.Cleanup(server.Close)
	return server
}

func plainSite(id string, port int, upstreamURL string) snapshot.Site {
	return snapshot.Site{ID: id, ListenAddress: "127.0.0.1", ListenPort: port, Upstreams: []snapshot.Upstream{{URL: upstreamURL}}}
}

func get(t *testing.T, port int, host string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+net.JoinHostPort("127.0.0.1", strconv.Itoa(port))+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func mustApply(t *testing.T, engine *Engine, sites ...snapshot.Site) {
	t.Helper()
	if errs := engine.Apply(sites); len(errs) > 0 {
		t.Fatalf("apply failed: %v", errs)
	}
}

func TestApplyStartsReplacesAndStopsSites(t *testing.T) {
	engine := newTestEngine(t)
	port := freePort(t)
	mustApply(t, engine, plainSite("a", port, textUpstream(t, "v1").URL))
	if got := get(t, port, ""); got != "v1" {
		t.Fatalf("first version served %q", got)
	}
	mustApply(t, engine, plainSite("a", port, textUpstream(t, "v2").URL))
	if got := get(t, port, ""); got != "v2" {
		t.Fatalf("reloaded version served %q", got)
	}
	mustApply(t, engine)
	if engine.Running("a") {
		t.Fatal("site absent from the snapshot kept running")
	}
	if _, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second); err == nil {
		t.Fatal("listener stayed bound after its last site stopped")
	}
}

func TestReloadKeepsInFlightStreamsOnTheirRuntime(t *testing.T) {
	release := make(chan struct{})
	streaming := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: first\n\n")
		http.NewResponseController(w).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: last\n\n")
	}))
	t.Cleanup(streaming.Close)
	engine := newTestEngine(t)
	port := freePort(t)
	mustApply(t, engine, plainSite("a", port, streaming.URL))

	resp, err := http.Get("http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	if line, _ := reader.ReadString('\n'); line != "data: first\n" {
		t.Fatalf("stream started with %q", line)
	}
	mustApply(t, engine, plainSite("a", port, textUpstream(t, "new").URL))
	if got := get(t, port, ""); got != "new" {
		t.Fatalf("new request after reload served %q", got)
	}
	close(release)
	rest, err := io.ReadAll(reader)
	if err != nil || !strings.Contains(string(rest), "data: last") {
		t.Fatalf("in-flight stream was cut by the reload: %q, %v", rest, err)
	}
}

func TestFailedApplyKeepsPreviousRuntime(t *testing.T) {
	engine := newTestEngine(t)
	port := freePort(t)
	good := plainSite("a", port, textUpstream(t, "good").URL)
	mustApply(t, engine, good)

	broken := good
	broken.Upstreams = []snapshot.Upstream{{URL: "not a url"}}
	if errs := engine.Apply([]snapshot.Site{broken}); errs["a"] == nil {
		t.Fatal("invalid upstream was accepted")
	}
	if got := get(t, port, ""); got != "good" {
		t.Fatalf("failed reload replaced the running site: %q", got)
	}

	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	moved := good
	moved.ListenPort = blocker.Addr().(*net.TCPAddr).Port
	if errs := engine.Apply([]snapshot.Site{moved}); errs["a"] == nil {
		t.Fatal("move to an occupied port was accepted")
	}
	if got := get(t, port, ""); got != "good" {
		t.Fatalf("failed move stopped the running site: %q", got)
	}
	if spec, _ := engine.Spec("a"); spec.ListenPort != port {
		t.Fatalf("engine reports spec for port %d, want %d", spec.ListenPort, port)
	}
}

func TestSharedListenerRejectsOverlappingHostnames(t *testing.T) {
	engine := newTestEngine(t)
	port := freePort(t)
	a := plainSite("a", port, textUpstream(t, "a").URL)
	a.Hostnames = []string{"*.example.test"}
	b := plainSite("b", port, textUpstream(t, "b").URL)
	b.Hostnames = []string{"b.test"}
	mustApply(t, engine, a, b)
	if get(t, port, "x.example.test") != "a" || get(t, port, "b.test") != "b" {
		t.Fatal("shared listener did not route by hostname")
	}
	c := plainSite("c", port, textUpstream(t, "c").URL)
	c.Hostnames = []string{"api.example.test"}
	errs := engine.Apply([]snapshot.Site{a, b, c})
	if errs["c"] == nil || !strings.Contains(errs["c"].Error(), "overlaps") {
		t.Fatalf("overlapping hostname was admitted: %v", errs)
	}
	if !engine.Running("a") || !engine.Running("b") || engine.Running("c") {
		t.Fatal("rejecting one site disturbed the others")
	}
}

// TestSitesCanTradeListenersInOneApply swaps two sites' listen ports within a single Apply call.
//
// The first Apply below is what actually binds portA/portB for the first time; freePort only proves a port was
// free at the moment it probed it (bind, note the number, close), so on a busy machine something else can grab
// that exact ephemeral port in the gap before this test gets around to binding it - a classic bind-then-later-
// use TOCTOU race in the test helper, not a listener-swap bug in the dataplane: Engine.install/shutdownGroup
// already close a retired listener synchronously (http.Server.Shutdown closes its listener as its first,
// synchronous step, see internal/dataplane/listeners.go) before any site tries to rebind that same address.
// Retrying with a freshly probed pair of ports on that rare failure is simpler and more reliable than trying to
// eliminate the race in freePort itself.
func TestSitesCanTradeListenersInOneApply(t *testing.T) {
	engine := newTestEngine(t)
	upA, upB := textUpstream(t, "a").URL, textUpstream(t, "b").URL
	var portA, portB int
	var errs map[string]error
	const attempts = 5
	for attempt := 1; attempt <= attempts; attempt++ {
		portA, portB = freePort(t), freePort(t)
		errs = engine.Apply([]snapshot.Site{plainSite("a", portA, upA), plainSite("b", portB, upB)})
		if len(errs) == 0 {
			break
		}
	}
	if len(errs) > 0 {
		t.Fatalf("apply failed after %d attempts with freshly probed ports: %v", attempts, errs)
	}
	mustApply(t, engine, plainSite("a", portB, upA), plainSite("b", portA, upB))
	if get(t, portA, "") != "b" || get(t, portB, "") != "a" {
		t.Fatal("sites did not trade listeners")
	}
}

type testCertificate struct{ certPEM, keyPEM string }

func newTestCertificate(t *testing.T) testCertificate {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	certificate := server.TLS.Certificates[0]
	keyDER, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	return testCertificate{
		certPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]})),
		keyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
	}
}

func TestCertificateOnlyChangeSwapsInPlace(t *testing.T) {
	engine := newTestEngine(t)
	port := freePort(t)
	first, second := newTestCertificate(t), newTestCertificate(t)
	site := plainSite("tls", port, textUpstream(t, "ok").URL)
	site.TLS, site.CertificateID = true, "shared"
	site.Certificate = &snapshot.KeyPair{CertificatePEM: first.certPEM, PrivateKeyPEM: first.keyPEM}
	mustApply(t, engine, site)
	presented := func() string {
		conn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: conn.ConnectionState().PeerCertificates[0].Raw}))
	}
	if presented() != first.certPEM {
		t.Fatal("site did not present its certificate")
	}
	engine.mu.Lock()
	before := engine.runs["tls"].route
	engine.mu.Unlock()
	site.Certificate = &snapshot.KeyPair{CertificatePEM: second.certPEM, PrivateKeyPEM: second.keyPEM}
	mustApply(t, engine, site)
	engine.mu.Lock()
	after := engine.runs["tls"].route
	engine.mu.Unlock()
	if before != after {
		t.Fatal("certificate renewal rebuilt the site runtime")
	}
	if presented() != second.certPEM {
		t.Fatal("renewed certificate was not presented")
	}
}

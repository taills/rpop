package control

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/sharedport"
	"github.com/rpop-project/rpop/internal/store"
)

// TestSetSharedPortRegistryLetsAnEmbeddedSiteShareTheConsolesAddress is stage one's end-to-end coverage for the
// injection point cmd/rpop.runController uses: a registry created outside Control, given to it with
// SetSharedPortRegistry before any site starts, ends up backing the embedded local node's engine, so a site
// placed on the local node can share the console's own listen address (told apart by hostname) instead of
// needing a port of its own.
func TestSetSharedPortRegistryLetsAnEmbeddedSiteShareTheConsolesAddress(t *testing.T) {
	s, _ := openSystemCATestStore(t)
	logDir := t.TempDir()
	c, err := NewWithLogDir(s, zap.NewNop(), logDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.StopAll()
		_ = c.CloseAccessLogs(context.Background())
	})

	registry := sharedport.NewRegistry()
	c.SetSharedPortRegistry(registry)

	port := freeLoopbackPort(t)
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	console := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "console") })
	if err := registry.PutPlaintextOwner(address, console, nil); err != nil {
		t.Fatalf("PutPlaintextOwner: %v", err)
	}
	t.Cleanup(func() { registry.RemovePlaintextOwner(address) })

	upstream := textServer(t, "site")
	site := store.Site{
		ID: "shared", Name: "shared",
		Config: store.Config{ListenAddress: "127.0.0.1", ListenPort: port, Hostnames: []string{"shared.test"}, Upstreams: []store.Upstream{{URL: upstream}}},
	}
	if err := s.Save(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if err := c.start(context.Background(), site.ID); err != nil {
		t.Fatalf("start: %v", err)
	}

	if got := httpGetHost(t, address, "shared.test"); got != "site" {
		t.Fatalf("shared.test routed to %q, want the site", got)
	}
	if got := httpGetHost(t, address, "console.test"); got != "console" {
		t.Fatalf("console.test routed to %q, want the console", got)
	}
}

func httpGetHost(t *testing.T, address, host string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+address+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

// httpGetHostStatus is httpGetHost but returns the status code instead of assuming 200, for the
// -console-hostnames "unknown Host gets a 404" case.
func httpGetHostStatus(t *testing.T, address, host string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+address+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// httpsGetSNI dials address over TLS with serverName as the SNI (and the sole name RootCAs trusts), for
// exercising a TLS site or owner sharing the console's address (see internal/sharedport's SNI dispatch).
func httpsGetSNI(t *testing.T, address string, certPEM []byte, serverName string) string {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("failed to parse test certificate PEM")
	}
	client := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: serverName}},
	}
	resp, err := client.Get("https://" + address + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

// TestSharedPortRegistryLetsATLSSiteShareTheConsolesAddress is stage one's end-to-end coverage, through the real
// cmd/rpop.runController wiring (Control + its embedded dataplane.Engine + a shared registry), for "a TLS site
// can share the console's port (told apart by SNI), while the console itself stays plaintext": a request for the
// site's own SNI/Host reaches the site over TLS, and a plaintext request for any other Host still falls back to
// the console, unaffected by the TLS site sharing its address.
//
// It also adds a *plaintext* site to the same address (stage three, see
// docs/architecture/control-data-plane.md §5, "共享端口(第三段)"): internal/dataplane.Engine forbade mixing a
// plaintext and a TLS *site* on one address from before shared ports existed until this relaxation — sharedport
// itself never needed the restriction, since it already tells the two encodings apart by the connection's first
// byte (see engine.go's install), which is exactly what a single-port deployment needs: one port serving both a
// plaintext and an HTTPS site. The three-way mix is proven at the layer that always allowed it,
// internal/sharedport.Registry itself, by TestPlainSiteTLSSiteAndPlaintextOwnerShareOneAddress; this test proves
// the same thing through the real Control/Engine wiring an operator actually runs.
func TestSharedPortRegistryLetsATLSSiteShareTheConsolesAddress(t *testing.T) {
	s, _ := openSystemCATestStore(t)
	logDir := t.TempDir()
	c, err := NewWithLogDir(s, zap.NewNop(), logDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.StopAll()
		_ = c.CloseAccessLogs(context.Background())
	})

	registry := sharedport.NewRegistry()
	c.SetSharedPortRegistry(registry)

	port := freeLoopbackPort(t)
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	console := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "console") })
	if err := registry.PutPlaintextOwner(address, console, nil); err != nil {
		t.Fatalf("PutPlaintextOwner: %v", err)
	}
	t.Cleanup(func() { registry.RemovePlaintextOwner(address) })

	material := makeTestTLSMaterial(t)
	tlsUpstream := textServer(t, "tls-site")
	tlsSite := store.Site{
		ID: "secure", Name: "secure",
		Config: store.Config{
			ListenAddress: "127.0.0.1", ListenPort: port, TLS: true, Hostnames: []string{"upstream.test"},
			CertificateSecret: "cert", PrivateKeySecret: "key", Upstreams: []store.Upstream{{URL: tlsUpstream}},
		},
	}
	if err := s.SaveSecret(context.Background(), tlsSite.ID, "cert", material.serverPEM); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSecret(context.Background(), tlsSite.ID, "key", material.serverKeyPEM); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(context.Background(), tlsSite); err != nil {
		t.Fatal(err)
	}
	if err := c.start(context.Background(), tlsSite.ID); err != nil {
		t.Fatalf("start tls site: %v", err)
	}

	plainUpstream := textServer(t, "plain-site")
	plainSite := store.Site{
		ID: "plain", Name: "plain",
		Config: store.Config{
			ListenAddress: "127.0.0.1", ListenPort: port, Hostnames: []string{"plain.test"},
			Upstreams: []store.Upstream{{URL: plainUpstream}},
		},
	}
	if err := s.Save(context.Background(), plainSite); err != nil {
		t.Fatal(err)
	}
	if err := c.start(context.Background(), plainSite.ID); err != nil {
		t.Fatalf("start plaintext site: %v", err)
	}

	if got := httpsGetSNI(t, address, material.serverPEM, "upstream.test"); got != "tls-site" {
		t.Fatalf("TLS SNI upstream.test routed to %q, want the TLS site", got)
	}
	if got := httpGetHost(t, address, "plain.test"); got != "plain-site" {
		t.Fatalf("plaintext plain.test routed to %q, want the plaintext site", got)
	}
	if got := httpGetHost(t, address, "console.test"); got != "console" {
		t.Fatalf("plaintext console.test routed to %q, want the console (unaffected by the TLS or plaintext site)", got)
	}
}

// TestConsoleHostnamesRestrictionReturns404ForUnknownHost covers -console-hostnames end to end: once the console
// is restricted to specific hostnames, a plaintext request for any Host neither a site nor the console's own
// restricted list claims gets a 404 instead of falling back to the console (see
// internal/sharedport.PutPlaintextOwner and dispatchPlain).
func TestConsoleHostnamesRestrictionReturns404ForUnknownHost(t *testing.T) {
	s, _ := openSystemCATestStore(t)
	logDir := t.TempDir()
	c, err := NewWithLogDir(s, zap.NewNop(), logDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.StopAll()
		_ = c.CloseAccessLogs(context.Background())
	})

	registry := sharedport.NewRegistry()
	c.SetSharedPortRegistry(registry)

	port := freeLoopbackPort(t)
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	console := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "console") })
	if err := registry.PutPlaintextOwner(address, console, []string{"console.test"}); err != nil {
		t.Fatalf("PutPlaintextOwner: %v", err)
	}
	t.Cleanup(func() { registry.RemovePlaintextOwner(address) })

	upstream := textServer(t, "site")
	site := store.Site{
		ID: "shared", Name: "shared",
		Config: store.Config{ListenAddress: "127.0.0.1", ListenPort: port, Hostnames: []string{"shared.test"}, Upstreams: []store.Upstream{{URL: upstream}}},
	}
	if err := s.Save(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if err := c.start(context.Background(), site.ID); err != nil {
		t.Fatalf("start: %v", err)
	}

	if got := httpGetHost(t, address, "shared.test"); got != "site" {
		t.Fatalf("shared.test routed to %q, want the site", got)
	}
	if got := httpGetHost(t, address, "console.test"); got != "console" {
		t.Fatalf("console.test routed to %q, want the console", got)
	}
	if status := httpGetHostStatus(t, address, "unknown.test"); status != http.StatusNotFound {
		t.Fatalf("unknown.test status = %d, want 404 once -console-hostnames restricts the console", status)
	}
}

// TestShutdownOrderUnregistersConsoleBeforeStoppingSitesAndFreesTheAddressLast covers cmd/rpop.runController's
// shutdown sequence: unregister the console first, then stop every site (StopAll), and only then does the shared
// address's real listener actually close — internal/sharedport.Registry does that step itself once nothing is
// left registered (see port.teardown), so there is no separate "close the listener" call to get wrong. A site
// sharing the address must keep serving after the console alone is unregistered, and the address must become
// immediately rebindable once StopAll removes the last thing on it — with no panic anywhere in the sequence.
func TestShutdownOrderUnregistersConsoleBeforeStoppingSitesAndFreesTheAddressLast(t *testing.T) {
	s, _ := openSystemCATestStore(t)
	logDir := t.TempDir()
	c, err := NewWithLogDir(s, zap.NewNop(), logDir)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			c.StopAll()
		}
		_ = c.CloseAccessLogs(context.Background())
	})

	registry := sharedport.NewRegistry()
	c.SetSharedPortRegistry(registry)

	port := freeLoopbackPort(t)
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	console := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "console") })
	if err := registry.PutPlaintextOwner(address, console, nil); err != nil {
		t.Fatalf("PutPlaintextOwner: %v", err)
	}

	upstream := textServer(t, "site")
	site := store.Site{
		ID: "shared", Name: "shared",
		Config: store.Config{ListenAddress: "127.0.0.1", ListenPort: port, Hostnames: []string{"shared.test"}, Upstreams: []store.Upstream{{URL: upstream}}},
	}
	if err := s.Save(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if err := c.start(context.Background(), site.ID); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Step 1: unregister the console. The site must keep serving; a Host neither the site nor (now, with no
	// owner registered at all) anything else claims gets sharedport's ownerless-port fallback, 421 Misdirected
	// Request (see internal/sharedport's servePlain) — not a hang or a panic.
	registry.RemovePlaintextOwner(address)
	if got := httpGetHost(t, address, "shared.test"); got != "site" {
		t.Fatalf("shared.test after unregistering the console: got %q, want the site still serving", got)
	}
	if status := httpGetHostStatus(t, address, "console.test"); status != http.StatusMisdirectedRequest {
		t.Fatalf("console.test after unregistering the console: status = %d, want %d", status, http.StatusMisdirectedRequest)
	}

	// Step 2: stop every site (StopAll). Only now should the address's real listener close.
	c.StopAll()
	closed = true

	deadline := time.Now().Add(2 * time.Second)
	for {
		ln, err := net.Listen("tcp", address)
		if err == nil {
			ln.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("address %s did not become free after StopAll: %v", address, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

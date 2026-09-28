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

	"github.com/rpop-project/rpop/internal/sharedport"
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
	// internal/sharedport.Registry.PutSite now makes this call, in Chinese (see that package's admit()); the
	// wording changed but the rejection itself did not.
	if errs["c"] == nil || !strings.Contains(errs["c"].Error(), "重叠") {
		t.Fatalf("overlapping hostname was admitted: %v", errs)
	}
	if !engine.Running("a") || !engine.Running("b") || engine.Running("c") {
		t.Fatal("rejecting one site disturbed the others")
	}
}

// getSNI GETs https://<address>/ with sni pinned as the ClientHello's SNI, skipping server certificate
// verification — this package's tests only care that sharedport's SNI dispatch reached the right site's
// handler, not about certificate trust (which internal/control's own tests already cover).
func getSNI(t *testing.T, address, sni string) string {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{ServerName: sni, InsecureSkipVerify: true}}}
	resp, err := client.Get("https://" + address + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

// TestSharedListenerAdmitsAPlaintextSiteAndATLSSite covers relaxing the engine's former "a shared address cannot
// mix a plaintext and a TLS site" restriction (see install's doc comment and
// docs/architecture/control-data-plane.md §5, "共享端口(第三段)"): sharedport.Registry.PutSite has always kept
// entirely separate plaintext/TLS site tables per address (told apart by the connection's first byte before
// either is reached, see internal/sharedport's doc comment), so once that was true, forbidding it here was a
// leftover from before shared ports existed rather than something either the registry or the wire protocol
// needed. This is exactly the single-port deployment scenario the relaxation exists for: one port serving both
// a plaintext and an HTTPS site.
func TestSharedListenerAdmitsAPlaintextSiteAndATLSSite(t *testing.T) {
	engine := newTestEngine(t)
	port := freePort(t)
	plain := plainSite("plain", port, textUpstream(t, "plain").URL)
	plain.Hostnames = []string{"plain.test"}
	cert := newTestCertificate(t)
	secure := plainSite("secure", port, textUpstream(t, "secure").URL)
	secure.TLS, secure.Hostnames = true, []string{"secure.test"}
	secure.Certificate = &snapshot.KeyPair{CertificatePEM: cert.certPEM, PrivateKeyPEM: cert.keyPEM}
	mustApply(t, engine, plain, secure)

	if got := get(t, port, "plain.test"); got != "plain" {
		t.Fatalf("plain.test routed to %q, want the plaintext site", got)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if got := getSNI(t, address, "secure.test"); got != "secure" {
		t.Fatalf("secure.test over TLS returned %q, want the TLS site", got)
	}
	if !engine.Running("plain") || !engine.Running("secure") {
		t.Fatal("both sites should be running")
	}
}

// TestSiteSwitchingFromPlaintextToTLSReleasesItsOldRegistration covers a site that stays on the same address but
// flips its TLS bit (e.g. an operator turns on HTTPS for an existing site without moving it): sharedport keeps
// entirely separate plaintext/TLS site tables per address (route.go), and PutSite only ever replaces the entry
// in the table matching the *new* registration's mode — so without install() explicitly detaching the old
// registration first, the site's stale plaintext entry would linger in plainSites forever, continuing to route
// requests through a runtime whose transports install already release()d. See
// docs/architecture/control-data-plane.md §5, "共享端口(第三段)".
func TestSiteSwitchingFromPlaintextToTLSReleasesItsOldRegistration(t *testing.T) {
	engine := newTestEngine(t)
	port := freePort(t)
	plain := plainSite("web", port, textUpstream(t, "plain").URL)
	mustApply(t, engine, plain)
	if got := get(t, port, ""); got != "plain" {
		t.Fatalf("plaintext GET before the switch = %q, want the plaintext site", got)
	}

	cert := newTestCertificate(t)
	secure := plainSite("web", port, textUpstream(t, "secure").URL)
	secure.TLS = true
	secure.Certificate = &snapshot.KeyPair{CertificatePEM: cert.certPEM, PrivateKeyPEM: cert.keyPEM}
	mustApply(t, engine, secure)

	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if got := getSNI(t, address, "irrelevant.test"); got != "secure" {
		t.Fatalf("TLS GET after the switch = %q, want the TLS site", got)
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + address + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("plaintext GET after switching to TLS-only still succeeded: %d %q, want the stale plaintext registration released", resp.StatusCode, body)
	}
}

// TestSiteSwitchingFromTLSToPlaintextReleasesItsOldRegistration is the mirror of the above: a TLS site turned
// plaintext must lose its stale tlsSites entry the same way, or a client that still dials it with TLS would keep
// reaching the old (released) runtime instead of a clean handshake failure.
func TestSiteSwitchingFromTLSToPlaintextReleasesItsOldRegistration(t *testing.T) {
	engine := newTestEngine(t)
	port := freePort(t)
	cert := newTestCertificate(t)
	secure := plainSite("web", port, textUpstream(t, "secure").URL)
	secure.TLS = true
	secure.Certificate = &snapshot.KeyPair{CertificatePEM: cert.certPEM, PrivateKeyPEM: cert.keyPEM}
	mustApply(t, engine, secure)
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if got := getSNI(t, address, "irrelevant.test"); got != "secure" {
		t.Fatalf("TLS GET before the switch = %q, want the TLS site", got)
	}

	plain := plainSite("web", port, textUpstream(t, "plain").URL)
	mustApply(t, engine, plain)

	if got := get(t, port, ""); got != "plain" {
		t.Fatalf("plaintext GET after the switch = %q, want the plaintext site", got)
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{ServerName: "irrelevant.test", InsecureSkipVerify: true}}}
	if _, err := client.Get("https://" + address + "/"); err == nil {
		t.Fatal("TLS GET after switching to plaintext-only unexpectedly succeeded, want the stale TLS registration released")
	}
}

// TestFailedModeSwitchKeepsServingTheOldMode covers D8 (a site that fails to apply keeps serving its previous
// config) for a mode switch specifically: install must detach the old mode's stale registration before it can
// even ask PutSite whether the new mode will be admitted (see the two tests above), so a PutSite failure here
// has to put the old registration back rather than leave the site registered under neither mode.
func TestFailedModeSwitchKeepsServingTheOldMode(t *testing.T) {
	engine := newTestEngine(t)
	port := freePort(t)
	web := plainSite("web", port, textUpstream(t, "plain").URL)
	cert := newTestCertificate(t)
	sibling := plainSite("sibling", port, textUpstream(t, "sibling").URL)
	sibling.TLS, sibling.Hostnames = true, []string{"sibling.test"}
	sibling.Certificate = &snapshot.KeyPair{CertificatePEM: cert.certPEM, PrivateKeyPEM: cert.keyPEM}
	mustApply(t, engine, web, sibling)

	// Switching "web" to TLS with no hostname must be rejected: the TLS side of this address already has
	// "sibling", which does have a hostname, so admit() requires "web" to configure one too in order to share.
	webTLS := plainSite("web", port, textUpstream(t, "web-tls").URL)
	webTLS.TLS = true
	webTLS.Certificate = &snapshot.KeyPair{CertificatePEM: cert.certPEM, PrivateKeyPEM: cert.keyPEM}
	errs := engine.Apply([]snapshot.Site{webTLS, sibling})
	if errs["web"] == nil {
		t.Fatal("expected the mode switch to fail admission")
	}

	if got := get(t, port, ""); got != "plain" {
		t.Fatalf("plaintext GET after the failed switch = %q, want the old plaintext site restored", got)
	}
	if !engine.Running("web") {
		t.Fatal("web should still be reported as running its old config")
	}
}

// TestFailedModeSwitchRestoreStopsTrackingTheSite covers the case TestFailedModeSwitchKeepsServingTheOldMode
// cannot: the restore PutSite that puts a site's old registration back after a failed mode switch can itself
// fail, if something else sharing the registry claims the vacated hostname in the instant between RemoveSite and
// the restore (see install's switchingMode). install must then stop tracking the site as running — Running()
// still claiming the site serves its old config once nothing is actually registered for it anymore would violate
// D8, not honor it. The race is engineered deterministically with the engine's own testAfterModeSwitchRemove test
// hook, since the real window between the two registry calls is far too small to hit reliably through the public
// API alone.
func TestFailedModeSwitchRestoreStopsTrackingTheSite(t *testing.T) {
	engine := newTestEngine(t)
	port := freePort(t)
	sibling := plainSite("sibling", port, textUpstream(t, "sibling").URL)
	sibling.Hostnames = []string{"sibling.test"}
	cert := newTestCertificate(t)
	web := plainSite("web", port, textUpstream(t, "web-tls").URL)
	web.TLS, web.Hostnames = true, []string{"web.test"}
	web.Certificate = &snapshot.KeyPair{CertificatePEM: cert.certPEM, PrivateKeyPEM: cert.keyPEM}
	mustApply(t, engine, sibling, web)

	grabberCert, err := tls.X509KeyPair([]byte(cert.certPEM), []byte(cert.keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	engine.testAfterModeSwitchRemove = func() {
		// Stand in for something else sharing the registry (an all-in-one process's console/southbound/relay
		// owner, or another site entirely) grabbing "web"'s just-vacated TLS hostname before install can restore
		// it, so the restore below fails too.
		route := sharedport.SiteRoute{
			ID: "grabber", Hostnames: []string{"web.test"}, TLS: true,
			Certificate: func() *tls.Certificate { return &grabberCert }, Handler: http.NotFoundHandler(),
		}
		if err := engine.registry.PutSite(address, route, nil); err != nil {
			t.Fatalf("grabber PutSite: %v", err)
		}
	}

	// Switching "web" to plaintext with no hostname must fail admission the same way the test above's does (the
	// plaintext side already has "sibling", which does have a hostname); the restore that would normally put
	// "web" back as TLS then collides with "grabber" and fails too.
	webPlain := plainSite("web", port, textUpstream(t, "web-plain").URL)
	errs := engine.Apply([]snapshot.Site{sibling, webPlain})
	if errs["web"] == nil {
		t.Fatal("expected the mode switch to fail admission")
	}

	if engine.Running("web") {
		t.Fatal("web should no longer be reported as running once its restore also failed")
	}
	engine.mu.Lock()
	group := engine.listeners[address]
	engine.mu.Unlock()
	if group != nil && group.siteIDs["web"] {
		t.Fatal("web's address group should no longer list it once its restore also failed")
	}
}

// TestEngineSharesAddressWithAPlaintextOwner is stage one's coverage for the injection point stage two (and the
// console, see cmd/rpop.runController) needs: an engine built with WithRegistry shares its registry with
// something else entirely outside the engine — here a plain http.Handler standing in for the console — and a
// site on the same address, given a hostname, routes correctly alongside it: the site's own hostname reaches
// the site, and everything else falls through to the owner, exactly as internal/sharedport's dispatchPlain
// documents.
func TestEngineSharesAddressWithAPlaintextOwner(t *testing.T) {
	registry := sharedport.NewRegistry()
	engine := New(zap.NewNop(), WithRegistry(registry))
	t.Cleanup(engine.StopAll)
	port := freePort(t)
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	owner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "owner") })
	if err := registry.PutPlaintextOwner(address, owner, nil); err != nil {
		t.Fatalf("PutPlaintextOwner: %v", err)
	}
	t.Cleanup(func() { registry.RemovePlaintextOwner(address) })

	site := plainSite("a", port, textUpstream(t, "site").URL)
	site.Hostnames = []string{"site.test"}
	mustApply(t, engine, site)

	if got := get(t, port, "site.test"); got != "site" {
		t.Fatalf("site.test routed to %q, want the site", got)
	}
	if got := get(t, port, "owner.test"); got != "owner" {
		t.Fatalf("owner.test routed to %q, want the owner", got)
	}
}

// TestEngineRejectsHostnamelessSiteSharingAnOwnersAddress mirrors
// TestSharedListenerRejectsOverlappingHostnames's "hostname required to share" case, but for sharing with an
// owner (the console) rather than with another site — internal/sharedport.Registry.PutSite enforces this the
// same way in both directions, and the engine must surface that error rather than somehow admitting the site.
func TestEngineRejectsHostnamelessSiteSharingAnOwnersAddress(t *testing.T) {
	registry := sharedport.NewRegistry()
	engine := New(zap.NewNop(), WithRegistry(registry))
	t.Cleanup(engine.StopAll)
	port := freePort(t)
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if err := registry.PutPlaintextOwner(address, http.NotFoundHandler(), nil); err != nil {
		t.Fatalf("PutPlaintextOwner: %v", err)
	}
	t.Cleanup(func() { registry.RemovePlaintextOwner(address) })

	errs := engine.Apply([]snapshot.Site{plainSite("a", port, textUpstream(t, "a").URL)})
	if errs["a"] == nil || !strings.Contains(errs["a"].Error(), "hostname") {
		t.Fatalf("expected a hostname-required error, got %v", errs)
	}
}

// TestEngineRejectsInternalHostnames covers pki.IsInternalHostname's own doc comment: the dataplane engine must
// reject a site hostname that collides with a name rpop's own certificates already own (the controller's own
// name, or the "*.nodes.rpop" node wildcard) before it ever reaches sharedport, with a clear Chinese error —
// otherwise a site could shadow the controller's or a node's own TLS identity if it ever shared a port with it.
func TestEngineRejectsInternalHostnames(t *testing.T) {
	engine := newTestEngine(t)
	port := freePort(t)
	for _, hostname := range []string{"controller.rpop", "edge-1.nodes.rpop", "*.nodes.rpop", "Controller.RPOP"} {
		site := plainSite("a", port, textUpstream(t, "a").URL)
		site.Hostnames = []string{hostname}
		errs := engine.Apply([]snapshot.Site{site})
		if errs["a"] == nil || !strings.Contains(errs["a"].Error(), "内部保留名") {
			t.Fatalf("hostname %q: expected an internal-hostname rejection, got %v", hostname, errs)
		}
		if engine.Running("a") {
			t.Fatalf("hostname %q: site should not be running after rejection", hostname)
		}
	}
}

// TestSitesCanTradeListenersInOneApply swaps two sites' listen ports within a single Apply call.
//
// The first Apply below is what actually binds portA/portB for the first time; freePort only proves a port was
// free at the moment it probed it (bind, note the number, close), so on a busy machine something else can grab
// that exact ephemeral port in the gap before this test gets around to binding it - a classic bind-then-later-
// use TOCTOU race in the test helper, not a listener-swap bug in the dataplane: Engine.install retires a site's
// old address through internal/sharedport.Registry.RemoveSite, which closes the real listener synchronously
// once nothing is left registered on it (see that package's port.teardown), before any site tries to rebind
// that same address.
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

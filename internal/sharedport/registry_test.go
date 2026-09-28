package sharedport

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func handlerBody(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) })
}

func httpGet(t *testing.T, address, host string, tlsConfig *tls.Config) (int, string) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	scheme := "http"
	if tlsConfig != nil {
		scheme = "https"
		client.Transport = &http.Transport{TLSClientConfig: tlsConfig}
	}
	req, err := http.NewRequest(http.MethodGet, scheme+"://"+address+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestPlaintextSiteOnlyRoutesLikeBeforeSharing(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	if err := registry.PutSite(address, SiteRoute{ID: "a", Handler: handlerBody("a")}, nil); err != nil {
		t.Fatalf("PutSite: %v", err)
	}
	if status, body := httpGet(t, address, "", nil); status != 200 || body != "a" {
		t.Fatalf("lone site with no hostname: status=%d body=%q", status, body)
	}
	registry.RemoveSite(address, "a")
	time.Sleep(50 * time.Millisecond)
	if _, err := net.DialTimeout("tcp", address, time.Second); err == nil {
		t.Fatal("address stayed bound after its last site left")
	}
}

func TestPlaintextSiteAndOwnerShareByHost(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	if err := registry.PutSite(address, SiteRoute{ID: "site", Hostnames: []string{"site.test"}, Handler: handlerBody("site")}, nil); err != nil {
		t.Fatalf("PutSite: %v", err)
	}
	if err := registry.PutPlaintextOwner(address, handlerBody("console"), nil); err != nil {
		t.Fatalf("PutPlaintextOwner: %v", err)
	}
	if status, body := httpGet(t, address, "site.test", nil); status != 200 || body != "site" {
		t.Fatalf("site.test: status=%d body=%q", status, body)
	}
	if status, body := httpGet(t, address, "anything-else.test", nil); status != 200 || body != "console" {
		t.Fatalf("fallback to console: status=%d body=%q", status, body)
	}
}

func TestPlaintextOwnerHostnamesRestrictIt(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	if err := registry.PutSite(address, SiteRoute{ID: "site", Hostnames: []string{"site.test"}, Handler: handlerBody("site")}, nil); err != nil {
		t.Fatalf("PutSite: %v", err)
	}
	if err := registry.PutPlaintextOwner(address, handlerBody("console"), []string{"Console.Test"}); err != nil {
		t.Fatalf("PutPlaintextOwner: %v", err)
	}
	if status, body := httpGet(t, address, "console.test", nil); status != 200 || body != "console" {
		t.Fatalf("console.test: status=%d body=%q", status, body)
	}
	if status, _ := httpGet(t, address, "unknown.test", nil); status != http.StatusNotFound {
		t.Fatalf("unknown host with a restricted owner: status=%d, want 404", status)
	}
}

func TestPlaintextSiteHostnameOverlappingOwnerRejected(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	if err := registry.PutPlaintextOwner(address, handlerBody("console"), []string{"console.test"}); err != nil {
		t.Fatalf("PutPlaintextOwner: %v", err)
	}
	err := registry.PutSite(address, SiteRoute{ID: "site", Hostnames: []string{"console.test"}, Handler: handlerBody("site")}, nil)
	if err == nil || !strings.Contains(err.Error(), "重叠") {
		t.Fatalf("expected an overlap error registering a site hostname that shadows the console, got %v", err)
	}
	// A wildcard site hostname that would shadow the console's exact hostname must be rejected the same way.
	err = registry.PutSite(address, SiteRoute{ID: "wild", Hostnames: []string{"*.test"}, Handler: handlerBody("wild")}, nil)
	if err == nil || !strings.Contains(err.Error(), "重叠") {
		t.Fatalf("expected an overlap error for a wildcard site hostname that shadows the console, got %v", err)
	}
}

func TestPlaintextOwnerHostnameOverlappingSiteRejected(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	if err := registry.PutSite(address, SiteRoute{ID: "site", Hostnames: []string{"site.test"}, Handler: handlerBody("site")}, nil); err != nil {
		t.Fatalf("PutSite: %v", err)
	}
	err := registry.PutPlaintextOwner(address, handlerBody("console"), []string{"site.test"})
	if err == nil || !strings.Contains(err.Error(), "重叠") {
		t.Fatalf("expected an overlap error registering a console hostname that shadows a site, got %v", err)
	}
}

func TestPlaintextSiteRequiresHostnameWhenSharingWithOwner(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	if err := registry.PutPlaintextOwner(address, handlerBody("console"), nil); err != nil {
		t.Fatalf("PutPlaintextOwner: %v", err)
	}
	err := registry.PutSite(address, SiteRoute{ID: "site", Handler: handlerBody("site")}, nil)
	if err == nil {
		t.Fatal("expected an error registering a hostname-less site alongside an owner")
	}
	if !strings.Contains(err.Error(), "hostname") {
		t.Fatalf("error does not mention hostname: %v", err)
	}
}

func TestPlaintextSiteRequiresHostnameWhenSharingWithAnotherSite(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	if err := registry.PutSite(address, SiteRoute{ID: "a", Hostnames: []string{"a.test"}, Handler: handlerBody("a")}, nil); err != nil {
		t.Fatalf("PutSite a: %v", err)
	}
	err := registry.PutSite(address, SiteRoute{ID: "b", Handler: handlerBody("b")}, nil)
	if err == nil {
		t.Fatal("expected an error for a hostname-less site sharing with a hostnamed one")
	}
}

func TestPlaintextOverlappingHostnamesRejected(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	if err := registry.PutSite(address, SiteRoute{ID: "a", Hostnames: []string{"*.example.test"}, Handler: handlerBody("a")}, nil); err != nil {
		t.Fatalf("PutSite a: %v", err)
	}
	err := registry.PutSite(address, SiteRoute{ID: "b", Hostnames: []string{"api.example.test"}, Handler: handlerBody("b")}, nil)
	if err == nil || !strings.Contains(err.Error(), "重叠") {
		t.Fatalf("expected an overlap error, got %v", err)
	}
}

func TestPlaintextIgnoreExemptsMovingSites(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	if err := registry.PutSite(address, SiteRoute{ID: "a", Hostnames: []string{"a.test"}, Handler: handlerBody("a")}, nil); err != nil {
		t.Fatalf("PutSite a: %v", err)
	}
	// "a" is about to leave this address in the same batch of changes, so registering "b" with no hostname (the
	// lone remaining occupant, from the caller's perspective) must not be rejected just because "a" is still
	// physically present until it is removed a moment later — mirroring dataplane's cross-listener churn.
	if err := registry.PutSite(address, SiteRoute{ID: "b", Handler: handlerBody("b")}, map[string]bool{"a": true}); err != nil {
		t.Fatalf("PutSite b with ignore: %v", err)
	}
	registry.RemoveSite(address, "a")
	if status, body := httpGet(t, address, "", nil); status != 200 || body != "b" {
		t.Fatalf("after a leaves: status=%d body=%q", status, body)
	}
}

func TestPlaintextRemovingOneSiteKeepsOthersServing(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	if err := registry.PutSite(address, SiteRoute{ID: "a", Hostnames: []string{"a.test"}, Handler: handlerBody("a")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := registry.PutSite(address, SiteRoute{ID: "b", Hostnames: []string{"b.test"}, Handler: handlerBody("b")}, nil); err != nil {
		t.Fatal(err)
	}
	registry.RemoveSite(address, "a")
	if status, body := httpGet(t, address, "b.test", nil); status != 200 || body != "b" {
		t.Fatalf("b.test after a removed: status=%d body=%q", status, body)
	}
	if status, _ := httpGet(t, address, "a.test", nil); status != http.StatusMisdirectedRequest {
		t.Fatalf("a.test after a removed: status=%d, want 421", status)
	}
}

// tlsClientConfig builds a client TLS config that trusts serverCert and, when clientCert is non-nil, presents it.
func tlsClientConfig(serverCert tls.Certificate, clientCert *tls.Certificate) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(serverCert.Leaf)
	cfg := &tls.Config{RootCAs: pool, ServerName: serverCert.Leaf.DNSNames[0]}
	if clientCert != nil {
		cfg.Certificates = []tls.Certificate{*clientCert}
	}
	return cfg
}

func TestTLSSiteOnlyRoutesBySNI(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	certA, certB := selfSignedCert(t, "a.test"), selfSignedCert(t, "b.test")
	if err := registry.PutSite(address, SiteRoute{ID: "a", Hostnames: []string{"a.test"}, TLS: true, Certificate: staticCert(certA), Handler: handlerBody("a")}, nil); err != nil {
		t.Fatalf("PutSite a: %v", err)
	}
	if err := registry.PutSite(address, SiteRoute{ID: "b", Hostnames: []string{"b.test"}, TLS: true, Certificate: staticCert(certB), Handler: handlerBody("b")}, nil); err != nil {
		t.Fatalf("PutSite b: %v", err)
	}
	if status, body := httpGet(t, address, "", tlsClientConfig(certA, nil)); status != 200 || body != "a" {
		t.Fatalf("a.test over TLS: status=%d body=%q", status, body)
	}
	if status, body := httpGet(t, address, "", tlsClientConfig(certB, nil)); status != 200 || body != "b" {
		t.Fatalf("b.test over TLS: status=%d body=%q", status, body)
	}
}

func TestTLSUnmatchedSNIFailsHandshake(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	certA, certUnknown := selfSignedCert(t, "a.test"), selfSignedCert(t, "unknown.test")
	if err := registry.PutSite(address, SiteRoute{ID: "a", Hostnames: []string{"a.test"}, TLS: true, Certificate: staticCert(certA), Handler: handlerBody("a")}, nil); err != nil {
		t.Fatalf("PutSite: %v", err)
	}
	conn, err := tls.Dial("tcp", address, tlsClientConfig(certUnknown, nil))
	if err == nil {
		conn.Close()
		t.Fatal("expected the handshake for an unmatched SNI to fail")
	}
}

func TestTLSOwnerAndSiteShareBySNI(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	ownerCert := selfSignedCert(t, "owner.test")
	siteCert := selfSignedCert(t, "site.test")

	ownerPool := x509.NewCertPool()
	ownerPool.AddCert(ownerCert.Leaf) // the owner only trusts its own certificate as a "client" cert, mimicking mTLS
	ownerTLSConfig := &tls.Config{
		Certificates: []tls.Certificate{ownerCert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: ownerPool,
	}
	listener, err := registry.PutTLSOwner(address, "owner.test", ownerTLSConfig)
	if err != nil {
		t.Fatalf("PutTLSOwner: %v", err)
	}
	owner := &http.Server{Handler: handlerBody("owner")}
	go func() { _ = owner.Serve(listener) }()
	t.Cleanup(func() { owner.Close() })

	if err := registry.PutSite(address, SiteRoute{ID: "site", Hostnames: []string{"site.test"}, TLS: true, Certificate: staticCert(siteCert), Handler: handlerBody("site")}, nil); err != nil {
		t.Fatalf("PutSite: %v", err)
	}

	// The owner requires a client certificate signed by its own leaf; a site connection presents none and must
	// not be able to reach the owner's handler or be rejected by the owner's ClientAuth requirement.
	if status, body := httpGet(t, address, "", tlsClientConfig(siteCert, nil)); status != 200 || body != "site" {
		t.Fatalf("site.test: status=%d body=%q", status, body)
	}
	// The owner's own SNI, with a valid client certificate, reaches the owner and not the site table.
	if status, body := httpGet(t, address, "", tlsClientConfig(ownerCert, &ownerCert)); status != 200 || body != "owner" {
		t.Fatalf("owner.test with client cert: status=%d body=%q", status, body)
	}
	// The owner's own SNI without a client certificate must fail the handshake (mTLS enforced), not silently fall
	// through to the site table. Under TLS 1.3 the client's own Handshake/Dial can return successfully even
	// though the server is about to reject the connection (the client sends its Finished flight, and only the
	// server's later, asynchronous alert reveals the missing certificate) — see RFC 8446 §4.4.2 and the
	// half-RTT client authentication trade-off it documents — so this drives an actual request instead of
	// trusting tls.Dial's error alone.
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsClientConfig(ownerCert, nil)}}
	resp, err := client.Get("https://" + address + "/")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected the owner's mTLS requirement to reject a connection with no client certificate")
	}
}

func TestTLSOwnerCloseFreesItsNameButKeepsSiteServing(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	ownerCert := selfSignedCert(t, "owner.test")
	siteCert := selfSignedCert(t, "site.test")
	listener, err := registry.PutTLSOwner(address, "owner.test", &tls.Config{Certificates: []tls.Certificate{ownerCert}})
	if err != nil {
		t.Fatalf("PutTLSOwner: %v", err)
	}
	if err := registry.PutSite(address, SiteRoute{ID: "site", Hostnames: []string{"site.test"}, TLS: true, Certificate: staticCert(siteCert), Handler: handlerBody("site")}, nil); err != nil {
		t.Fatalf("PutSite: %v", err)
	}
	listener.Close()
	// The site must still be reachable: the owner leaving does not tear down the whole port.
	if status, body := httpGet(t, address, "", tlsClientConfig(siteCert, nil)); status != 200 || body != "site" {
		t.Fatalf("site after owner closed: status=%d body=%q", status, body)
	}
	if conn, err := tls.Dial("tcp", address, tlsClientConfig(ownerCert, nil)); err == nil {
		conn.Close()
		t.Fatal("owner's SNI should no longer resolve after it closed its listener")
	}
}

func TestLastParticipantFreesTheAddressImmediately(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	for i := 0; i < 20; i++ {
		cert := selfSignedCert(t, "owner.test")
		listener, err := registry.PutTLSOwner(address, "owner.test", &tls.Config{Certificates: []tls.Certificate{cert}})
		if err != nil {
			t.Fatalf("attempt %d: PutTLSOwner: %v", i, err)
		}
		listener.Close()
	}
}

func TestPeekTimeoutClosesSilentConnections(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	if err := registry.PutSite(address, SiteRoute{ID: "a", Handler: handlerBody("a")}, nil); err != nil {
		t.Fatal(err)
	}
	// dispatch's PeekTimeout is 8s in production; a unit test cannot wait that long, so this test only proves a
	// silent connection is eventually closed by the server side rather than left open forever - it dials, writes
	// nothing, and expects a read to eventually observe EOF/reset well before any sane request timeout,
	// without asserting on the exact production duration.
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if pt := PeekTimeout; pt <= 0 || pt > time.Minute {
		t.Fatalf("PeekTimeout = %v, want a small positive bound", pt)
	}
}

func TestConcurrentPutRemoveIsRace_Free(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := "site"
			route := SiteRoute{ID: id, Handler: handlerBody("x")}
			for j := 0; j < 25; j++ {
				_ = registry.PutSite(address, route, nil)
				registry.RemoveSite(address, id)
			}
		}(i)
	}
	wg.Wait()
}

func TestPutSiteRejectsTLSWithoutCertificate(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	err := registry.PutSite(address, SiteRoute{ID: "a", TLS: true, Handler: handlerBody("a")}, nil)
	if err == nil {
		t.Fatal("expected an error for a TLS site with no certificate getter")
	}
}

func TestPutPlaintextOwnerRejectsASecondOwner(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	if err := registry.PutPlaintextOwner(address, handlerBody("first"), nil); err != nil {
		t.Fatal(err)
	}
	if err := registry.PutPlaintextOwner(address, handlerBody("second"), nil); err == nil {
		t.Fatal("expected an error registering a second plaintext owner")
	}
}

func TestPutTLSOwnerRejectsADuplicateName(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	cert := selfSignedCert(t, "owner.test")
	listener, err := registry.PutTLSOwner(address, "owner.test", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := registry.PutTLSOwner(address, "owner.test", &tls.Config{Certificates: []tls.Certificate{cert}}); err == nil {
		t.Fatal("expected an error registering a duplicate TLS owner name")
	}
}

func TestBindFailurePropagates(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	registry := NewRegistry()
	if err := registry.PutSite(blocker.Addr().String(), SiteRoute{ID: "a", Handler: handlerBody("a")}, nil); err == nil {
		t.Fatal("expected a bind error for an address already in use")
	}
}

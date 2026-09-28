package sharedport

import (
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"testing"
)

// dialNoSNI completes a TLS handshake against address sending no SNI at all (an explicitly empty
// tls.Config.ServerName; crypto/tls omits the SNI extension whenever ServerName is empty or parses as an IP —
// see hostnameInSNI in the standard library) — what a node whose binary predates pinning the bootstrap SNI to
// pki.ControllerName sends when its -controller URL names a literal IP host (see pki.BootstrapClientConfig's doc
// comment for the fixed, current behavior: it pins ServerName explicitly, so a current node's bootstrap dial
// sends SNI = pki.ControllerName regardless of the -controller URL's own host, and no longer takes this path).
func dialNoSNI(t *testing.T, address string, tlsConfig *tls.Config) (*tls.Conn, error) {
	t.Helper()
	raw, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	cfg := tlsConfig.Clone()
	cfg.ServerName = ""
	conn := tls.Client(raw, cfg)
	if err := conn.Handshake(); err != nil {
		raw.Close()
		return nil, err
	}
	return conn, nil
}

func TestDefaultTLSOwnerServesUnmatchedAndEmptySNI(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	defaultCert := selfSignedCert(t, "default.test")
	listener, err := registry.PutDefaultTLSOwner(address, &tls.Config{Certificates: []tls.Certificate{defaultCert}})
	if err != nil {
		t.Fatalf("PutDefaultTLSOwner: %v", err)
	}
	owner := &http.Server{Handler: handlerBody("default")}
	go func() { _ = owner.Serve(listener) }()
	t.Cleanup(func() { owner.Close() })

	pool := func() *tls.Config { c := &tls.Config{InsecureSkipVerify: true}; return c }
	// No SNI at all (e.g. a bootstrap dial from a node old enough to predate pinning the SNI, against a
	// -controller URL naming a literal IP — see dialNoSNI's doc comment).
	conn, err := dialNoSNI(t, address, pool())
	if err != nil {
		t.Fatalf("handshake with no SNI: %v", err)
	}
	conn.Close()
	// An SNI that matches nothing registered on this address.
	unmatched := pool()
	unmatched.ServerName = "unknown.test"
	if status, body := httpGet(t, address, "", unmatched); status != 200 || body != "default" {
		t.Fatalf("unmatched SNI: status=%d body=%q", status, body)
	}
}

func TestDefaultTLSOwnerLosesToExactOwnerAndSite(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	defaultCert := selfSignedCert(t, "default.test")
	defaultListener, err := registry.PutDefaultTLSOwner(address, &tls.Config{Certificates: []tls.Certificate{defaultCert}})
	if err != nil {
		t.Fatalf("PutDefaultTLSOwner: %v", err)
	}
	defaultOwner := &http.Server{Handler: handlerBody("default")}
	go func() { _ = defaultOwner.Serve(defaultListener) }()
	t.Cleanup(func() { defaultOwner.Close() })

	ownerCert := selfSignedCert(t, "owner.test")
	ownerListener, err := registry.PutTLSOwner(address, "owner.test", &tls.Config{Certificates: []tls.Certificate{ownerCert}})
	if err != nil {
		t.Fatalf("PutTLSOwner: %v", err)
	}
	owner := &http.Server{Handler: handlerBody("owner")}
	go func() { _ = owner.Serve(ownerListener) }()
	t.Cleanup(func() { owner.Close() })

	siteCert := selfSignedCert(t, "site.test")
	if err := registry.PutSite(address, SiteRoute{ID: "site", Hostnames: []string{"site.test"}, TLS: true, Certificate: staticCert(siteCert), Handler: handlerBody("site")}, nil); err != nil {
		t.Fatalf("PutSite: %v", err)
	}

	if status, body := httpGet(t, address, "", tlsClientConfig(ownerCert, nil)); status != 200 || body != "owner" {
		t.Fatalf("owner.test: status=%d body=%q, want the exact TLS owner, not the default one", status, body)
	}
	if status, body := httpGet(t, address, "", tlsClientConfig(siteCert, nil)); status != 200 || body != "site" {
		t.Fatalf("site.test: status=%d body=%q, want the TLS site, not the default owner", status, body)
	}
	unmatched := &tls.Config{InsecureSkipVerify: true, ServerName: "unknown.test"}
	if status, body := httpGet(t, address, "", unmatched); status != 200 || body != "default" {
		t.Fatalf("unknown.test: status=%d body=%q, want the default owner", status, body)
	}
}

func TestDefaultTLSOwnerRejectsWhenLoneHostnamelessTLSSiteExists(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	siteCert := selfSignedCert(t, "site.test")
	if err := registry.PutSite(address, SiteRoute{ID: "site", TLS: true, Certificate: staticCert(siteCert), Handler: handlerBody("site")}, nil); err != nil {
		t.Fatalf("PutSite: %v", err)
	}
	_, err := registry.PutDefaultTLSOwner(address, &tls.Config{Certificates: []tls.Certificate{selfSignedCert(t, "default.test")}})
	if err == nil {
		t.Fatal("expected an error registering a default TLS owner alongside a hostname-less TLS site")
	}
	if !strings.Contains(err.Error(), "hostname") {
		t.Fatalf("error does not mention hostname: %v", err)
	}
}

func TestPutSiteRequiresHostnameWhenSharingWithDefaultTLSOwner(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	if _, err := registry.PutDefaultTLSOwner(address, &tls.Config{Certificates: []tls.Certificate{selfSignedCert(t, "default.test")}}); err != nil {
		t.Fatalf("PutDefaultTLSOwner: %v", err)
	}
	siteCert := selfSignedCert(t, "site.test")
	err := registry.PutSite(address, SiteRoute{ID: "site", TLS: true, Certificate: staticCert(siteCert), Handler: handlerBody("site")}, nil)
	if err == nil {
		t.Fatal("expected an error registering a hostname-less TLS site alongside a default TLS owner")
	}
	if !strings.Contains(err.Error(), "hostname") {
		t.Fatalf("error does not mention hostname: %v", err)
	}
}

func TestPutDefaultTLSOwnerRejectsASecondOwner(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	cert := selfSignedCert(t, "default.test")
	listener, err := registry.PutDefaultTLSOwner(address, &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := registry.PutDefaultTLSOwner(address, &tls.Config{Certificates: []tls.Certificate{cert}}); err == nil {
		t.Fatal("expected an error registering a second default TLS owner")
	}
}

func TestDefaultTLSOwnerCloseFreesItButKeepsSiteServing(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	defaultCert := selfSignedCert(t, "default.test")
	listener, err := registry.PutDefaultTLSOwner(address, &tls.Config{Certificates: []tls.Certificate{defaultCert}})
	if err != nil {
		t.Fatalf("PutDefaultTLSOwner: %v", err)
	}
	owner := &http.Server{Handler: handlerBody("default")}
	go func() { _ = owner.Serve(listener) }()
	t.Cleanup(func() { owner.Close() })

	siteCert := selfSignedCert(t, "site.test")
	if err := registry.PutSite(address, SiteRoute{ID: "site", Hostnames: []string{"site.test"}, TLS: true, Certificate: staticCert(siteCert), Handler: handlerBody("site")}, nil); err != nil {
		t.Fatalf("PutSite: %v", err)
	}
	listener.Close()

	if status, body := httpGet(t, address, "", tlsClientConfig(siteCert, nil)); status != 200 || body != "site" {
		t.Fatalf("site after default owner closed: status=%d body=%q", status, body)
	}
	unmatched := &tls.Config{InsecureSkipVerify: true, ServerName: "unknown.test"}
	conn, err := tls.Dial("tcp", address, unmatched)
	if err == nil {
		conn.Close()
		t.Fatal("expected the handshake for an unmatched SNI to fail once the default owner closed")
	}
}

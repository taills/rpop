package sharedport

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"testing"
	"time"
)

// TestTLSOwnerNegotiatesHTTP2 proves that handing a completed *tls.Conn to an owner's own *http.Server through
// this package's virtual listener (registry.go's plainListener/tlsListener trick: dispatch.go completes the
// handshake itself, so the owner's server only ever calls Serve, never ServeTLS) still lets HTTP/2 negotiate and
// actually carry a request/response — not just that ALPN picked "h2" during the handshake. A node's relay port
// (the reason PutTLSOwner exists) requires HTTP/2 end to end, so this is load-bearing for stage two, not a nicety.
func TestTLSOwnerNegotiatesHTTP2(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	ownerCert := selfSignedCert(t, "owner.test")

	// Offering only "h2" (no "http/1.1" fallback) mirrors internal/pki.Identity.RelayServerConfig's ALPN, so a
	// successful round trip here can only have happened over HTTP/2.
	ownerTLSConfig := &tls.Config{Certificates: []tls.Certificate{ownerCert}, NextProtos: []string{"h2"}}
	listener, err := registry.PutTLSOwner(address, "owner.test", ownerTLSConfig)
	if err != nil {
		t.Fatalf("PutTLSOwner: %v", err)
	}
	owner := &http.Server{Handler: handlerBody("owner-h2")}
	go func() { _ = owner.Serve(listener) }()
	t.Cleanup(func() { owner.Close() })

	pool := x509.NewCertPool()
	pool.AddCert(ownerCert.Leaf)
	client := &http.Client{
		Timeout: 3 * time.Second,
		// ForceAttemptHTTP2 is required here: net/http.Transport only auto-negotiates HTTP/2 on its own when
		// no custom TLSClientConfig is set (see Transport.protocols, Go issue 14275) — a real h2 client (an
		// overlay link's dialer, or a browser) requests h2 explicitly via ALPN, which this reproduces.
		Transport: &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "owner.test"}},
	}
	resp, err := client.Get("https://" + address + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("response negotiated HTTP/%d.%d, want HTTP/2", resp.ProtoMajor, resp.ProtoMinor)
	}
	if resp.TLS == nil || resp.TLS.NegotiatedProtocol != "h2" {
		t.Fatalf("TLS NegotiatedProtocol = %q, want %q", resp.TLS.NegotiatedProtocol, "h2")
	}
}

// TestTLSOwnerMTLSOverHTTP2 combines what stage two's relay port actually needs from a TLS owner: mutual TLS
// (RequireAndVerifyClientCert, as internal/pki.Identity.RelayServerConfig sets) and HTTP/2, in the same
// handshake, over the same virtual-listener hand-off TestTLSOwnerNegotiatesHTTP2 exercises alone. A client
// presenting a trusted certificate completes an h2 round trip; the same dial with no client certificate must
// still be rejected (mTLS enforced even though the connection also negotiates h2).
func TestTLSOwnerMTLSOverHTTP2(t *testing.T) {
	registry := NewRegistry()
	address := freeTCPAddr(t)
	ownerCert := selfSignedCert(t, "owner.test")
	clientCert := selfSignedCert(t, "peer.nodes.rpop")

	pool := x509.NewCertPool()
	pool.AddCert(clientCert.Leaf)
	ownerTLSConfig := &tls.Config{
		Certificates: []tls.Certificate{ownerCert}, NextProtos: []string{"h2"},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool,
	}
	listener, err := registry.PutTLSOwner(address, "owner.test", ownerTLSConfig)
	if err != nil {
		t.Fatalf("PutTLSOwner: %v", err)
	}
	owner := &http.Server{Handler: handlerBody("owner-mtls-h2")}
	go func() { _ = owner.Serve(listener) }()
	t.Cleanup(func() { owner.Close() })

	serverPool := x509.NewCertPool()
	serverPool.AddCert(ownerCert.Leaf)
	authed := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{
			RootCAs: serverPool, ServerName: "owner.test", Certificates: []tls.Certificate{clientCert},
		}},
	}
	resp, err := authed.Get("https://" + address + "/")
	if err != nil {
		t.Fatalf("GET with client certificate: %v", err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("response negotiated HTTP/%d.%d, want HTTP/2", resp.ProtoMajor, resp.ProtoMinor)
	}
	if resp.TLS == nil || resp.TLS.NegotiatedProtocol != "h2" {
		t.Fatalf("TLS NegotiatedProtocol = %q, want %q", resp.TLS.NegotiatedProtocol, "h2")
	}

	// No client certificate: as TestTLSOwnerAndSiteShareBySNI's comment explains, TLS 1.3 can let the client's
	// own handshake report success before the server's rejection reaches it, so this drives an actual request
	// rather than trusting the dial/handshake return alone.
	unauthed := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{RootCAs: serverPool, ServerName: "owner.test"}},
	}
	if resp, err := unauthed.Get("https://" + address + "/"); err == nil {
		resp.Body.Close()
		t.Fatal("expected the owner's mTLS requirement to reject a connection with no client certificate")
	}
}

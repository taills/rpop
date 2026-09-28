package overlay

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/snapshot"
)

// attemptRelayHandshake completes (or fails to complete) a TLS handshake against the relay port at address, then
// drives one write/read to force the server's async rejection to actually surface: under TLS 1.3 the client's own
// Handshake can report success before the server's later, asynchronous alert reveals a missing/invalid client
// certificate (RFC 8446 §4.4.2's half-RTT trade-off — see TestTLSOwnerAndSiteShareBySNI's identical caveat in
// internal/sharedport), so tls.Dial's own error is not trusted alone.
func attemptRelayHandshake(t *testing.T, address string, cfg *tls.Config) error {
	t.Helper()
	conn, err := tls.Dial("tcp", address, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("x")); err != nil {
		return err
	}
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	return err
}

// TestRelaySharedPortRejectsUnauthenticatedConnections proves the relay port's mTLS requirement
// (pki.Identity.RelayServerConfig's RequireAndVerifyClientCert) still holds once the relay port is registered
// as an internal/sharedport TLS owner (PutTLSOwner) instead of driving its own net.Listener: sharedport's
// dispatch.go completes the handshake with the owner's tls.Config exactly as the owner configured it, so this
// is really a regression check on stage two's relay.go rewrite, not a retest of sharedport's own generic mTLS
// coverage (TestTLSOwnerMTLSOverHTTP2).
//
// Every dial below pins ServerName to the relay's own exact-SNI owner name (pki.NodeName, see relay.go's
// startRelay): address is a literal IP ("127.0.0.1:<port>", from freeAddress), and crypto/tls only derives SNI
// from the dial host when Config.ServerName is left empty (see tls.Dial/DialWithDialer), so an unset ServerName
// here would send no SNI at all — dispatch.go would then never reach the relay's TLS owner in the first place
// (no exact owner, no TLS site, no default owner registered on this private, relay-only registry — see doc.go's
// "no match fails the handshake"), and every assertion below would pass or fail for that reason instead of the
// one it claims to test.
func TestRelaySharedPortRejectsUnauthenticatedConnections(t *testing.T) {
	ca, err := pki.NewCA("relay reject test CA")
	if err != nil {
		t.Fatal(err)
	}
	relay := newOverlay(t, identityFor(t, ca, "node1", 1))
	address := freeAddress(t)
	if err := relay.Apply(snapshot.Snapshot{
		NodeID: "node1", RelayListen: address,
		Relay: []snapshot.RelayRoute{{Key: "k", From: []string{"node2"}, Target: "127.0.0.1:1"}},
	}); err != nil {
		t.Fatal(err)
	}
	relaySNI := pki.NodeName("node1")

	if err := attemptRelayHandshake(t, address, &tls.Config{InsecureSkipVerify: true, ServerName: relaySNI, NextProtos: []string{"h2"}}); err == nil {
		t.Fatal("expected the relay port to reject a connection with no client certificate")
	}

	otherCA, err := pki.NewCA("a different overlay test CA")
	if err != nil {
		t.Fatal(err)
	}
	foreign := identityFor(t, otherCA, "node9", 1)
	cfg := &tls.Config{InsecureSkipVerify: true, ServerName: relaySNI, NextProtos: []string{"h2"}, Certificates: []tls.Certificate{*foreign.Certificate()}}
	if err := attemptRelayHandshake(t, address, cfg); err == nil {
		t.Fatal("expected the relay port to reject a client certificate issued by a different CA")
	}

	// A legitimate peer must still be able to complete the handshake and open a stream over the same address.
	peer := identityFor(t, ca, "node2", 1)
	cfg = &tls.Config{InsecureSkipVerify: true, ServerName: relaySNI, NextProtos: []string{"h2"}, Certificates: []tls.Certificate{*peer.Certificate()}}
	conn, err := tls.Dial("tcp", address, cfg)
	if err != nil {
		t.Fatalf("expected an authorized peer's handshake to succeed: %v", err)
	}
	conn.Close()
}

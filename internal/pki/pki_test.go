package pki

import (
	"crypto/tls"
	"net"
	"strings"
	"testing"
)

func newNode(t *testing.T, ca *CA, nodeID string) *Identity {
	t.Helper()
	keyPEM, csrPEM, err := NewKeyAndCSR(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	_, certPEM, err := ca.SignNode(csrPEM, nodeID, 1)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := LoadIdentity(certPEM, keyPEM, ca.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

// handshake runs one TLS handshake over an in-memory pipe and returns both sides' errors.
func handshake(server, client *tls.Config) (serverState tls.ConnectionState, serverErr, clientErr error) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn := tls.Server(a, server)
		serverErr = conn.Handshake()
		serverState = conn.ConnectionState()
		if serverErr != nil {
			_ = a.Close()
		}
	}()
	conn := tls.Client(b, client)
	clientErr = conn.Handshake()
	if clientErr != nil {
		_ = b.Close()
	}
	<-done
	return serverState, serverErr, clientErr
}

func TestCARoundTripAndNodeCertificates(t *testing.T) {
	ca, err := NewCA("rpop test CA")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCA(ca.CertPEM, ca.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Fingerprint() != ca.Fingerprint() || len(ca.Fingerprint()) != 64 {
		t.Fatalf("fingerprint changed across load: %s vs %s", loaded.Fingerprint(), ca.Fingerprint())
	}
	node := newNode(t, loaded, "edge-1")
	if node.NodeID != "edge-1" {
		t.Fatalf("identity node = %q", node.NodeID)
	}
	if id, ok := NodeIDFromCertificate(node.Certificate().Leaf); !ok || id != "edge-1" {
		t.Fatalf("certificate names node %q", id)
	}
	if generation, ok := NodeGeneration(node.Certificate().Leaf); !ok || generation != 1 {
		t.Fatalf("certificate generation = %d, %v", generation, ok)
	}
	_, unsigned, _ := NewKeyAndCSR("edge-1")
	if _, _, err := ca.SignNode(unsigned, "edge-1", 0); err == nil {
		t.Fatal("a certificate without a generation was issued")
	}
	other, err := NewCA("other CA")
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, csrPEM, _ := NewKeyAndCSR("edge-1")
	_, foreignPEM, _ := other.SignNode(csrPEM, "edge-1", 1)
	if _, err := LoadIdentity(foreignPEM, keyPEM, ca.CertPEM); err == nil {
		t.Fatal("a certificate from another CA was accepted")
	}
}

func TestRelayMutualTLSVerifiesPeerIdentity(t *testing.T) {
	ca, _ := NewCA("rpop test CA")
	relay, dialer := newNode(t, ca, "relay"), newNode(t, ca, "ingress")
	generation1 := func() (int64, bool) { return 1, true }
	state, serverErr, clientErr := handshake(relay.RelayServerConfig(), dialer.PeerClientConfig("relay", generation1))
	if serverErr != nil || clientErr != nil {
		t.Fatalf("handshake failed: server %v client %v", serverErr, clientErr)
	}
	if peer, ok := PeerNodeID(&state); !ok || peer != "ingress" {
		t.Fatalf("relay saw peer %q", peer)
	}
	if _, _, clientErr := handshake(relay.RelayServerConfig(), dialer.PeerClientConfig("someone-else", generation1)); clientErr == nil {
		t.Fatal("dialer accepted a relay certificate for another node")
	}
	outsider := newNode(t, mustCA(t), "ingress")
	if _, serverErr, _ := handshake(relay.RelayServerConfig(), outsider.PeerClientConfig("relay", generation1)); serverErr == nil {
		t.Fatal("relay accepted a client certificate from another CA")
	}
}

// TestPeerClientConfigVerifiesTheServersRegistrationGeneration covers D26: a node that re-registers gets a new
// certificate generation, and every node dialing it must reject its previous one, even if the process holding
// the old key is still listening and otherwise a valid, CA-signed peer.
func TestPeerClientConfigVerifiesTheServersRegistrationGeneration(t *testing.T) {
	ca, _ := NewCA("rpop test CA")
	relay, dialer := newNode(t, ca, "relay"), newNode(t, ca, "ingress")
	matching := func() (int64, bool) { return 1, true }
	if _, _, clientErr := handshake(relay.RelayServerConfig(), dialer.PeerClientConfig("relay", matching)); clientErr != nil {
		t.Fatalf("dial at the matching generation failed: %v", clientErr)
	}
	revoked := func() (int64, bool) { return 2, true }
	if _, _, clientErr := handshake(relay.RelayServerConfig(), dialer.PeerClientConfig("relay", revoked)); clientErr == nil {
		t.Fatal("dialer accepted a relay certificate from an earlier, now-revoked generation")
	}
	unknown := func() (int64, bool) { return 0, false }
	if _, _, clientErr := handshake(relay.RelayServerConfig(), dialer.PeerClientConfig("relay", unknown)); clientErr == nil {
		t.Fatal("dialer accepted a relay certificate for a peer that is not in its current snapshot")
	}
}

func TestBootstrapPinsTheCAFingerprint(t *testing.T) {
	ca := mustCA(t)
	controllerCert, err := ca.IssueController()
	if err != nil {
		t.Fatal(err)
	}
	server := &tls.Config{Certificates: []tls.Certificate{controllerCert}}
	if _, _, clientErr := handshake(server, BootstrapClientConfig(ca.Fingerprint())); clientErr != nil {
		t.Fatalf("bootstrap rejected the pinned controller: %v", clientErr)
	}
	if _, _, clientErr := handshake(server, BootstrapClientConfig(mustCA(t).Fingerprint())); clientErr == nil {
		t.Fatal("bootstrap accepted a controller outside the pinned CA")
	}
}

func TestJoinTokenRoundTrip(t *testing.T) {
	token := JoinToken{NodeID: "edge-1", Secret: strings.Repeat("a", 43), CAFingerprint: strings.Repeat("f", 64)}
	parsed, err := ParseJoinToken(" " + token.String() + "\n")
	if err != nil || parsed != token {
		t.Fatalf("parsed %#v, %v", parsed, err)
	}
	for _, bad := range []string{"", "rpop2.a.b.c", "rpop1.edge.short." + strings.Repeat("f", 64), "rpop1..s.f"} {
		if _, err := ParseJoinToken(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func mustCA(t *testing.T) *CA {
	t.Helper()
	ca, err := NewCA("rpop test CA")
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

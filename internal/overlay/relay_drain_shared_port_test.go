package overlay

import (
	"crypto/tls"
	"io"
	"net/http"
	"testing"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/sharedport"
	"github.com/rpop-project/rpop/internal/snapshot"
)

// TestRelayDrainLeavesASharedSiteUnaffectedAndRelayRestartsImmediately covers stage two's registry.PutTLSOwner
// rewrite of the relay port (relay.go's startRelay/drain) against the one thing TestRelayDrainFreesTheAddressImmediately
// cannot see: a relay port that shares its address with something else on the same internal/sharedport.Registry
// (a node's own site, told apart by Host — see internal/agent's TestSiteSharesItsNodesOwnRelayPort for the
// full end-to-end version of this). drain must unregister only the relay's own TLS-owner name (PutTLSOwner's
// Close), never the shared address's underlying listener while another registration still needs it (see
// port.teardown); and, symmetrically, the relay must be able to come back up at the very same address right
// away, exactly as TestRelayDrainFreesTheAddressImmediately already proves when nothing else shares the port.
func TestRelayDrainLeavesASharedSiteUnaffectedAndRelayRestartsImmediately(t *testing.T) {
	ca, err := pki.NewCA("relay drain shared port test CA")
	if err != nil {
		t.Fatal(err)
	}
	registry := sharedport.NewRegistry()
	o := New(identityFor(t, ca, "node1", 1), zap.NewNop(), DefaultConfig(), WithRegistry(registry))
	address := freeAddress(t)

	// A plaintext owner mimics the plaintext site a real node would place on its own relay address (see
	// internal/dataplane.Engine's PutSite calls, wired to the same registry by internal/agent.Agent). Unlike
	// PutTLSOwner, PutPlaintextOwner drives handler through the registry's own shared *http.Server for the
	// address's plaintext side, so there is no listener of our own to serve.
	siteHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "site is up") })
	if err := registry.PutPlaintextOwner(address, siteHandler, nil); err != nil {
		t.Fatalf("PutPlaintextOwner: %v", err)
	}
	t.Cleanup(func() { registry.RemovePlaintextOwner(address) })

	getSite := func() string {
		t.Helper()
		resp, err := http.Get("http://" + address + "/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}
	if body := getSite(); body != "site is up" {
		t.Fatalf("site before the relay ever starts: %q", body)
	}

	relaySNI := pki.NodeName("node1")
	peer := identityFor(t, ca, "node2", 1)
	relayCfg := &tls.Config{InsecureSkipVerify: true, ServerName: relaySNI, NextProtos: []string{"h2"}, Certificates: []tls.Certificate{*peer.Certificate()}}
	relayRoute := snapshot.Snapshot{
		NodeID: "node1", RelayListen: address,
		Relay: []snapshot.RelayRoute{{Key: "k", From: []string{"node2"}, Target: "127.0.0.1:1"}},
	}

	// Start the relay: it now shares address with the site above.
	if err := o.Apply(relayRoute); err != nil {
		t.Fatal(err)
	}
	if conn, err := tls.Dial("tcp", address, relayCfg); err != nil {
		t.Fatalf("relay handshake while sharing the address with the site: %v", err)
	} else {
		conn.Close()
	}
	if body := getSite(); body != "site is up" {
		t.Fatalf("site while the relay is active: %q", body)
	}

	// Drain the relay (no more relay routes ⇒ applyRelayLocked drains and forgets it, see Overlay.Apply).
	if err := o.Apply(snapshot.Snapshot{NodeID: "node1"}); err != nil {
		t.Fatal(err)
	}

	// The site must be reachable immediately: drain unregisters only the relay's own TLS-owner name (see
	// tlsOwnerListener.Close/port.removeTLSOwner), never the address's shared listener the site still needs.
	if body := getSite(); body != "site is up" {
		t.Fatalf("site right after the relay drained: %q", body)
	}
	// The relay's own SNI must no longer resolve to anything.
	if conn, err := tls.Dial("tcp", address, relayCfg); err == nil {
		conn.Close()
		t.Fatal("expected the relay's SNI to stop resolving once it drained")
	}

	// The relay must be able to restart at the very same address right away, alongside the untouched site —
	// the shared-address analogue of TestRelayDrainFreesTheAddressImmediately.
	if err := o.Apply(relayRoute); err != nil {
		t.Fatalf("relay restart at the same shared address: %v", err)
	}
	if conn, err := tls.Dial("tcp", address, relayCfg); err != nil {
		t.Fatalf("relay handshake after restart: %v", err)
	} else {
		conn.Close()
	}
	if body := getSite(); body != "site is up" {
		t.Fatalf("site after the relay restarted: %q", body)
	}
}

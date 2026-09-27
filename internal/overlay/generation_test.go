package overlay

import (
	"testing"

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/snapshot"
)

// TestDialRejectsAPeerCertificateFromAnEarlierGeneration covers D26 revocation from the dialing side: a node
// re-registering bumps its certificate generation, and every other node must reject its previous one even
// though the process holding the old key is still listening and is otherwise a valid, CA-signed peer.
func TestDialRejectsAPeerCertificateFromAnEarlierGeneration(t *testing.T) {
	ca, err := pki.NewCA("overlay test CA")
	if err != nil {
		t.Fatal(err)
	}
	target := startLineEcho(t)
	relayAddr := freeAddress(t)
	const key = "route-1"
	// node2 is still serving under its real, generation-1 certificate.
	node2 := newOverlay(t, identityFor(t, ca, "node2", 1))
	apply(t, node2, snapshot.Snapshot{NodeID: "node2", Peers: []snapshot.Peer{{ID: "node1", Generation: 1}}, RelayListen: relayAddr,
		Relay: []snapshot.RelayRoute{{Key: key, From: []string{"node1"}, Target: target}}})

	// The controller believes node2 re-registered at generation 2, so the ingress's snapshot expects that.
	ingress := newOverlay(t, identityFor(t, ca, "node1", 1))
	path := snapshot.Path{Key: key, Label: "node2", FirstNode: "node2", Target: target}
	apply(t, ingress, snapshot.Snapshot{NodeID: "node1", Peers: []snapshot.Peer{{ID: "node2", Address: relayAddr, Generation: 2}},
		Sites: []snapshot.Site{{ID: "s", Upstreams: []snapshot.Upstream{{URL: "http://" + target, Paths: []snapshot.Path{path}}}}}})
	if _, err := dial(t, ingress, path); err == nil {
		t.Fatal("dial succeeded against a peer certificate from an earlier, now-revoked generation")
	}

	// Once the ingress's snapshot agrees with node2's real generation again, the same route works: this shows
	// the earlier failure came from the generation mismatch and not some other break.
	apply(t, ingress, snapshot.Snapshot{NodeID: "node1", Peers: []snapshot.Peer{{ID: "node2", Address: relayAddr, Generation: 1}},
		Sites: []snapshot.Site{{ID: "s", Upstreams: []snapshot.Upstream{{URL: "http://" + target, Paths: []snapshot.Path{path}}}}}})
	conn, err := dial(t, ingress, path)
	if err != nil {
		t.Fatalf("dial at the matching generation = %v, want success", err)
	}
	conn.Close()
}

// TestApplyRetiresALinkWhenThePeersExpectedGenerationChanges covers a link already up when its peer's snapshot
// row moves to a new generation (the controller revoked and re-registered it): the link must not go on serving
// tunnels under the certificate it verified before the change.
func TestApplyRetiresALinkWhenThePeersExpectedGenerationChanges(t *testing.T) {
	ca, err := pki.NewCA("overlay test CA")
	if err != nil {
		t.Fatal(err)
	}
	target := startLineEcho(t)
	relayAddr := freeAddress(t)
	const key = "route-1"
	node2 := newOverlay(t, identityFor(t, ca, "node2", 1))
	apply(t, node2, snapshot.Snapshot{NodeID: "node2", Peers: []snapshot.Peer{{ID: "node1", Generation: 1}}, RelayListen: relayAddr,
		Relay: []snapshot.RelayRoute{{Key: key, From: []string{"node1"}, Target: target}}})

	ingress := newOverlay(t, identityFor(t, ca, "node1", 1))
	path := snapshot.Path{Key: key, Label: "node2", FirstNode: "node2", Target: target}
	apply(t, ingress, snapshot.Snapshot{NodeID: "node1", Peers: []snapshot.Peer{{ID: "node2", Address: relayAddr, Generation: 1}},
		Sites: []snapshot.Site{{ID: "s", Upstreams: []snapshot.Upstream{{URL: "http://" + target, Paths: []snapshot.Path{path}}}}}})
	conn, err := dial(t, ingress, path)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if links := ingress.Links(); len(links) != 1 || links[0].Connections == 0 {
		t.Fatalf("ingress links before the generation change = %#v, want one warm link", links)
	}

	// The controller now believes node2 re-registered at generation 2, even though the real node2 process here
	// still presents its generation-1 certificate (as a revoked node's old process would).
	apply(t, ingress, snapshot.Snapshot{NodeID: "node1", Peers: []snapshot.Peer{{ID: "node2", Address: relayAddr, Generation: 2}},
		Sites: []snapshot.Site{{ID: "s", Upstreams: []snapshot.Upstream{{URL: "http://" + target, Paths: []snapshot.Path{path}}}}}})
	if _, err := dial(t, ingress, path); err == nil {
		t.Fatal("dial succeeded through a link created for an earlier, now-revoked generation")
	}
}

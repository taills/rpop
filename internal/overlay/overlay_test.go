package overlay

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/snapshot"
)

func identityFor(t *testing.T, ca *pki.CA, id string, generation int64) *pki.Identity {
	t.Helper()
	keyPEM, csrPEM, err := pki.NewKeyAndCSR(id)
	if err != nil {
		t.Fatal(err)
	}
	_, certPEM, err := ca.SignNode(csrPEM, id, generation)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := pki.LoadIdentity(certPEM, keyPEM, ca.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

// startLineEcho answers every line with "echo: <line>" as soon as it arrives, and "bye" when the client
// half-closes.
func startLineEcho(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				scanner := bufio.NewScanner(conn)
				for scanner.Scan() {
					if _, err := io.WriteString(conn, "echo: "+scanner.Text()+"\n"); err != nil {
						return
					}
				}
				io.WriteString(conn, "bye\n")
			}()
		}
	}()
	return listener.Addr().String()
}

func newOverlay(t *testing.T, identity *pki.Identity) *Overlay {
	t.Helper()
	o := New(identity, zap.NewNop())
	t.Cleanup(o.Close)
	return o
}

func apply(t *testing.T, o *Overlay, s snapshot.Snapshot) {
	t.Helper()
	if err := o.Apply(s); err != nil {
		t.Fatal(err)
	}
}

// chain is node1 -> socks5-A -> node2 -> node3 -> socks5-B -> target, with node1 opening the tunnel.
type chain struct {
	ingress             *Overlay
	path                snapshot.Path
	socksA, socksB      *testSocks
	relayAddr, exitAddr string
	relay, exit         *Overlay
	ca                  *pki.CA
	relaySnapshot       snapshot.Snapshot
}

func newChain(t *testing.T, target string) *chain {
	t.Helper()
	ca, err := pki.NewCA("overlay test CA")
	if err != nil {
		t.Fatal(err)
	}
	c := &chain{ca: ca, socksA: startSocks(t, "alice", "secret"), socksB: startSocks(t, "", ""), relayAddr: freeAddress(t), exitAddr: freeAddress(t)}
	socksA := snapshot.Proxy{Type: "socks5h", Address: c.socksA.addr(), Username: "alice", Password: "secret"}
	socksB := snapshot.Proxy{Type: "socks5", Address: c.socksB.addr()}
	const key = "route-1"
	c.exit = newOverlay(t, identityFor(t, ca, "node3", 1))
	apply(t, c.exit, snapshot.Snapshot{NodeID: "node3", Peers: []snapshot.Peer{{ID: "node2", Generation: 1}}, RelayListen: c.exitAddr,
		Relay: []snapshot.RelayRoute{{Key: key, From: []string{"node2"}, Egress: []snapshot.Proxy{socksB}, Target: target}}})
	c.relay = newOverlay(t, identityFor(t, ca, "node2", 1))
	c.relaySnapshot = snapshot.Snapshot{NodeID: "node2", Peers: []snapshot.Peer{{ID: "node1", Generation: 1}, {ID: "node3", Address: c.exitAddr, Generation: 1}},
		RelayListen: c.relayAddr, Relay: []snapshot.RelayRoute{{Key: key, From: []string{"node1"}, Next: "node3", Target: target}}}
	apply(t, c.relay, c.relaySnapshot)
	c.path = snapshot.Path{Key: key, Label: "proxy:a > node2 > node3 > proxy:b", FirstNode: "node2", LinkProxies: []snapshot.Proxy{socksA}, Target: target}
	c.ingress = newOverlay(t, identityFor(t, ca, "node1", 1))
	apply(t, c.ingress, snapshot.Snapshot{NodeID: "node1", Peers: []snapshot.Peer{{ID: "node2", Address: c.relayAddr, Generation: 1}},
		Sites: []snapshot.Site{{ID: "s", Upstreams: []snapshot.Upstream{{URL: "http://" + target, Paths: []snapshot.Path{c.path}}}}}})
	return c
}

func dial(t *testing.T, o *Overlay, path snapshot.Path) (net.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return o.DialPath(ctx, path)
}

func readLine(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := r.ReadString('\n')
		done <- result{line, err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("read: %v", res.err)
		}
		return strings.TrimSuffix(res.line, "\n")
	case <-time.After(3 * time.Second):
		t.Fatal("a line sent through the tunnel was held back")
		return ""
	}
}

func TestTunnelCrossesProxiesAndRelaysWithoutBuffering(t *testing.T) {
	target := startLineEcho(t)
	c := newChain(t, target)
	conn, err := dial(t, c.ingress, c.path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	// Lockstep: each answer must arrive before the next line is sent, so any buffering on any hop stalls here.
	for _, word := range []string{"one", "two", "three"} {
		if _, err := io.WriteString(conn, word+"\n"); err != nil {
			t.Fatal(err)
		}
		if got := readLine(t, reader); got != "echo: "+word {
			t.Fatalf("got %q", got)
		}
	}
	if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if got := readLine(t, reader); got != "bye" {
		t.Fatalf("after half-close got %q", got)
	}
	if got := c.socksA.targets(); len(got) != 1 || got[0] != c.relayAddr {
		t.Fatalf("socks5h link proxy was asked for %v, want %s", got, c.relayAddr)
	}
	if got := c.socksB.targets(); len(got) != 1 || got[0] != target {
		t.Fatalf("socks5 egress proxy was asked for %v, want %s", got, target)
	}
	links := c.ingress.Links()
	if len(links) != 1 || links[0].Peer != "node2" || links[0].Connections < 1 {
		t.Fatalf("ingress links = %#v", links)
	}
}

func TestLinksStayWarmAndCarryManyTunnels(t *testing.T) {
	target := startLineEcho(t)
	c := newChain(t, target)
	deadline := time.Now().Add(5 * time.Second)
	for c.ingress.Links()[0].Connections == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the link was not dialed before the first tunnel")
		}
		time.Sleep(10 * time.Millisecond)
	}
	conns := make([]net.Conn, 20)
	for i := range conns {
		conn, err := dial(t, c.ingress, c.path)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conns[i] = conn
	}
	for i, conn := range conns {
		io.WriteString(conn, "ping\n")
		if got := readLine(t, bufio.NewReader(conn)); got != "echo: ping" {
			t.Fatalf("tunnel %d got %q", i, got)
		}
	}
	if status := c.ingress.Links()[0]; status.Connections != 1 || status.Tunnels != len(conns) {
		t.Fatalf("link status = %#v, want 20 tunnels on one connection", status)
	}
}

func TestRelayRefusesTunnelsItWasNotConfiguredFor(t *testing.T) {
	target := startLineEcho(t)
	c := newChain(t, target)
	expectRefusal := func(path snapshot.Path, status int) {
		t.Helper()
		conn, err := dial(t, c.ingress, path)
		var relayErr *RelayError
		if !errors.As(err, &relayErr) || relayErr.Status != status {
			if conn != nil {
				conn.Close()
			}
			t.Fatalf("dial error = %v, want relay status %d", err, status)
		}
	}
	unknown := c.path
	unknown.Key = "route-that-does-not-exist"
	expectRefusal(unknown, http.StatusForbidden)

	fromElsewhere := c.relaySnapshot
	fromElsewhere.Relay = []snapshot.RelayRoute{{Key: c.path.Key, From: []string{"node9"}, Next: "node3", Target: target}}
	apply(t, c.relay, fromElsewhere)
	expectRefusal(c.path, http.StatusForbidden)

	revoked := c.relaySnapshot
	revoked.Peers = []snapshot.Peer{{ID: "node1", Generation: 2}, {ID: "node3", Address: c.exitAddr, Generation: 1}}
	apply(t, c.relay, revoked)
	expectRefusal(c.path, http.StatusForbidden)

	apply(t, c.relay, c.relaySnapshot)
	conn, err := dial(t, c.ingress, c.path)
	if err != nil {
		t.Fatalf("restored route: %v", err)
	}
	conn.Close()
}

func TestDialChainThroughHTTPAndSocksProxies(t *testing.T) {
	target := startLineEcho(t)
	httpProxy := startHTTPProxy(t, "bob", "pw")
	socks := startSocks(t, "", "")
	proxies := []snapshot.Proxy{{Type: "http", Address: httpProxy, Username: "bob", Password: "pw"}, {Type: "socks5h", Address: socks.addr()}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := DialChain(ctx, proxies, target)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "hi\n")
	if got := readLine(t, bufio.NewReader(conn)); got != "echo: hi" {
		t.Fatalf("got %q", got)
	}
	proxies[0].Password = "wrong"
	if _, err := DialChain(ctx, proxies, target); err == nil || !strings.Contains(err.Error(), "407") {
		t.Fatalf("bad proxy credentials: %v", err)
	}
}

func TestDialPathWithoutNodesUsesEgressProxies(t *testing.T) {
	target := startLineEcho(t)
	socks := startSocks(t, "", "")
	o := newOverlay(t, nil)
	conn, err := dial(t, o, snapshot.Path{Label: "proxy:s", Egress: []snapshot.Proxy{{Type: "socks5h", Address: socks.addr()}}, Target: target})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "x\n")
	if got := readLine(t, bufio.NewReader(conn)); got != "echo: x" {
		t.Fatalf("got %q", got)
	}
}

// TestTunnelsFailFastWhileADownLinkRedials checks the link-layer health signal: once a link has failed, a tunnel
// does not wait for the background redial, which can hang until the handshake timeout, so the ingress moves on
// to its next path at once.
func TestTunnelsFailFastWhileADownLinkRedials(t *testing.T) {
	ca, err := pki.NewCA("overlay test CA")
	if err != nil {
		t.Fatal(err)
	}
	address := freeAddress(t)
	ingress := newOverlay(t, identityFor(t, ca, "node1", 1))
	path := snapshot.Path{Key: "k", Label: "node2", FirstNode: "node2", Target: "127.0.0.1:1"}
	apply(t, ingress, snapshot.Snapshot{NodeID: "node1", Peers: []snapshot.Peer{{ID: "node2", Address: address, Generation: 1}},
		Sites: []snapshot.Site{{ID: "s", Upstreams: []snapshot.Upstream{{URL: "http://example.test", Paths: []snapshot.Path{path}}}}}})
	deadline := time.Now().Add(5 * time.Second)
	for ingress.Links()[0].Failures == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the link to a closed port never failed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The peer comes back as a black hole: it accepts TCP and never answers the TLS handshake.
	blackHole, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer blackHole.Close()
	var mu sync.Mutex
	var held []net.Conn
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range held {
			conn.Close()
		}
	}()
	accepted := make(chan struct{}, 1)
	go func() {
		for {
			conn, err := blackHole.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
			select {
			case accepted <- struct{}{}:
			default:
			}
		}
	}()
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("the link was not redialed")
	}
	start := time.Now()
	if _, err := dial(t, ingress, path); !errors.Is(err, errLinkDown) {
		t.Fatalf("dial while the down link redials = %v, want errLinkDown", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("the tunnel waited %v for a redial of a link known to be down", waited)
	}
}

// TestLinkComesUpSoonAfterItsPeerStartsListening covers nodes applying one revision at the same moment: a relay
// dials its next hop before that node's relay port is up, and the link must not stay down for long after.
func TestLinkComesUpSoonAfterItsPeerStartsListening(t *testing.T) {
	ca, err := pki.NewCA("overlay test CA")
	if err != nil {
		t.Fatal(err)
	}
	target := startLineEcho(t)
	relayAddr := freeAddress(t)
	const key = "route-1"
	path := snapshot.Path{Key: key, Label: "node2", FirstNode: "node2", Target: target}
	ingress := newOverlay(t, identityFor(t, ca, "node1", 1))
	apply(t, ingress, snapshot.Snapshot{NodeID: "node1", Peers: []snapshot.Peer{{ID: "node2", Address: relayAddr, Generation: 1}},
		Sites: []snapshot.Site{{ID: "s", Upstreams: []snapshot.Upstream{{URL: "http://" + target, Paths: []snapshot.Path{path}}}}}})
	deadline := time.Now().Add(5 * time.Second)
	for ingress.Links()[0].Failures == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the link to a port nobody listens on never failed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	relay := newOverlay(t, identityFor(t, ca, "node2", 1))
	apply(t, relay, snapshot.Snapshot{NodeID: "node2", Peers: []snapshot.Peer{{ID: "node1", Generation: 1}}, RelayListen: relayAddr,
		Relay: []snapshot.RelayRoute{{Key: key, From: []string{"node1"}, Target: target}}})
	started := time.Now()
	for ingress.Links()[0].Connections == 0 {
		if time.Since(started) > 800*time.Millisecond {
			t.Fatalf("the link was still down %v after its peer started listening", time.Since(started))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

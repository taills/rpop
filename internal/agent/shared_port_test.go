package agent

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// getHost is get, but with an explicit Host header — the equivalent of `curl -H 'Host: ...'` — needed to reach a
// plaintext site that shares its listener with something else (here, the node's own relay port) by hostname
// instead of being the address's only occupant.
func getHost(t *testing.T, target, host string) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = host
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return string(body)
}

// TestSiteSharesItsNodesOwnRelayPort covers stage two of shared-port: a node's own relay port (an exact-SNI TLS
// owner of internal/sharedport.Registry, see internal/overlay/relay.go's startRelay) and a plaintext site
// placed on that same node can share one address, told apart by the connection's first byte and then, for the
// plaintext site, its Host header — exactly like a site sharing an address with the console already did in
// stage one, just with the relay port as the other occupant instead. It also proves the relay endpoint on that
// shared address keeps working: a second node's site, routed through the first node as a relay hop, reaches the
// same origin over a real mTLS tunnel concurrently with the local site's plaintext traffic.
func TestSiteSharesItsNodesOwnRelayPort(t *testing.T) {
	c := startController(t)
	relayAddr := freeAddress(t)
	tokenA := c.createRelayNode("edge-a", relayAddr)
	tokenB := c.createNode("edge-b")

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "from origin") }))
	defer origin.Close()

	// RelayListen pins edge-a's relay bind to the exact address its site below also listens on: the controller
	// otherwise renders a wildcard bind for the port alone (relayListenAddress in internal/control/paths.go,
	// so a relay stays reachable regardless of which local interface a peer's relayAddress names), which would
	// conflict with the site's specific host:port (internal/sharedport.Classify's Conflicting case) instead of
	// sharing it.
	startAgent(t, Config{ControllerURL: c.southbound.URL, JoinToken: tokenA, DataDir: t.TempDir(), Version: "test", RelayListen: relayAddr})
	edgeB, _ := startAgent(t, Config{ControllerURL: c.southbound.URL, JoinToken: tokenB, DataDir: t.TempDir(), Version: "test"})
	eventually(t, "both nodes register", func() bool { return c.inSync("edge-a", "edge-b") })

	host, portStr, err := net.SplitHostPort(relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	relayPort, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	localSite := fmt.Sprintf(`{"id":"local-web","name":"local-web","config":{"nodes":["edge-a"],"hostnames":["local.test"],"listenAddress":%q,"listenPort":%d,
		"upstreams":[{"url":%q}]}}`, host, relayPort, origin.URL)
	c.call(http.MethodPost, "/api/sites", localSite, http.StatusCreated)
	c.call(http.MethodPost, "/api/sites/local-web/start", "", http.StatusOK)

	relayedPort := freePort(t)
	relayedSite := fmt.Sprintf(`{"id":"relayed-web","name":"relayed-web","config":{"nodes":["edge-b"],"listenAddress":"127.0.0.1","listenPort":%d,
		"upstreams":[{"url":%q,"via":[{"node":"edge-a"}]}]}}`, relayedPort, origin.URL)
	c.call(http.MethodPost, "/api/sites", relayedSite, http.StatusCreated)
	c.call(http.MethodPost, "/api/sites/relayed-web/start", "", http.StatusOK)

	eventually(t, "both sites are running and in sync", func() bool {
		return c.inSync("edge-a", "edge-b")
	})
	eventually(t, "edge-b's link to edge-a is up", func() bool { return linksUp(edgeB) })

	if body := getHost(t, "http://"+relayAddr+"/", "local.test"); body != "from origin" {
		t.Fatalf("site sharing the relay's own address: body = %q", body)
	}
	target := fmt.Sprintf("http://127.0.0.1:%d/", relayedPort)
	eventually(t, "the relayed site reaches the origin through edge-a's shared address", func() bool {
		return probe(target) == "from origin"
	})
}

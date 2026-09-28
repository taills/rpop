package agent

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
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

// getSNI GETs target over TLS with sni pinned as the ClientHello's SNI — the equivalent of `curl --resolve` plus
// an explicit `--connect-to`'s ServerName — needed to reach a TLS site that shares its listener with something
// else (here, the node's own relay port, itself an exact-SNI TLS owner) by hostname instead of being the
// address's only occupant. Server certificate verification is skipped: the test only cares that sharedport's
// SNI-based dispatch reaches the right handler, not about certificate trust, which internal/control's own tests
// already cover.
func getSNI(t *testing.T, address, sni string) string {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{ServerName: sni, InsecureSkipVerify: true}}}
	response, err := client.Get("https://" + address + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return string(body)
}

// encodeTestCertificate PEM-encodes a certificate httptest.NewTLSServer minted, for use as a site's certificate
// secret (PUT /api/sites/{id}/secrets/{name}). The certificate's own hostname does not need to match the site's
// configured hostname: getSNI skips server certificate verification, exactly like internal/control's identically
// named test helper (control_test.go) does for the same reason.
func encodeTestCertificate(t *testing.T, cert tls.Certificate) (certPEM, keyPEM []byte) {
	t.Helper()
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// TestTLSSiteSharesItsNodesOwnRelayPort covers the TLS-site half of stage two's shared-port story: a node's own
// relay port (an exact-SNI TLS owner) and a TLS site placed on that same node can share one address, told apart
// entirely by SNI — the relay's own exact name (pki.NodeName) on one side, the site's configured hostname on the
// other (dispatch.go's getConfigForClient tries the exact owner first, then TLS sites; see internal/sharedport's
// TestTLSOwnerAndSiteShareBySNI for the same match order at the package's own level). It also proves the relay
// endpoint keeps forwarding tunnels for another node concurrently with the TLS site's own traffic. Adding a
// plaintext site to the very same address too (stage three, see
// docs/architecture/control-data-plane.md §5, "共享端口(第三段)") is covered by
// TestPlaintextAndTLSSitesShareTheirNodesOwnRelayPort right below, once
// internal/dataplane.Engine stopped forbidding a plaintext and a TLS *site* from sharing one address.
func TestTLSSiteSharesItsNodesOwnRelayPort(t *testing.T) {
	c := startController(t)
	relayAddr := freeAddress(t)
	tokenA := c.createRelayNode("edge-a", relayAddr)
	tokenB := c.createNode("edge-b")

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "from origin") }))
	defer origin.Close()

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

	certServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer certServer.Close()
	certPEM, keyPEM := encodeTestCertificate(t, certServer.TLS.Certificates[0])

	secureSite := fmt.Sprintf(`{"id":"secure-web","name":"secure-web","config":{"nodes":["edge-a"],"hostnames":["secure.test"],"listenAddress":%q,"listenPort":%d,
		"tls":true,"certificateSecret":"cert","privateKeySecret":"key","upstreams":[{"url":%q}]}}`, host, relayPort, origin.URL)
	c.call(http.MethodPost, "/api/sites", secureSite, http.StatusCreated)
	c.call(http.MethodPut, "/api/sites/secure-web/secrets/cert", string(certPEM), http.StatusCreated)
	c.call(http.MethodPut, "/api/sites/secure-web/secrets/key", string(keyPEM), http.StatusCreated)
	c.call(http.MethodPost, "/api/sites/secure-web/start", "", http.StatusOK)

	relayedPort := freePort(t)
	relayedSite := fmt.Sprintf(`{"id":"relayed-web","name":"relayed-web","config":{"nodes":["edge-b"],"listenAddress":"127.0.0.1","listenPort":%d,
		"upstreams":[{"url":%q,"via":[{"node":"edge-a"}]}]}}`, relayedPort, origin.URL)
	c.call(http.MethodPost, "/api/sites", relayedSite, http.StatusCreated)
	c.call(http.MethodPost, "/api/sites/relayed-web/start", "", http.StatusOK)

	eventually(t, "both sites are running and in sync", func() bool {
		return c.inSync("edge-a", "edge-b")
	})
	eventually(t, "edge-b's link to edge-a is up", func() bool { return linksUp(edgeB) })

	if body := getSNI(t, relayAddr, "secure.test"); body != "from origin" {
		t.Fatalf("TLS site sharing the relay's own address: body = %q", body)
	}
	target := fmt.Sprintf("http://127.0.0.1:%d/", relayedPort)
	eventually(t, "the relayed site reaches the origin through edge-a's shared address", func() bool {
		return probe(target) == "from origin"
	})
}

// TestPlaintextAndTLSSitesShareTheirNodesOwnRelayPort covers stage three's relaxation of
// internal/dataplane.Engine's former "a shared address cannot mix a plaintext and a TLS site" restriction (see
// docs/architecture/control-data-plane.md §5, "共享端口(第三段)"): a plaintext site, a TLS site, and a node's
// own relay port (an exact-SNI TLS owner) all share one address at once, each told apart by the connection's
// first byte and then, within each encoding, by Host/SNI — sharedport.Registry always allowed this (see
// TestPlainSiteTLSSiteAndPlaintextOwnerShareOneAddress), it was only the engine's own site-to-site rule, a
// leftover from before shared ports existed, that stood in the way. TestSiteSharesItsNodesOwnRelayPort and
// TestTLSSiteSharesItsNodesOwnRelayPort above already prove the plaintext-plus-relay and TLS-plus-relay pairs
// individually; this test is the three-way combination, plus the same "a relayed site through edge-a keeps
// working" check both of them already make.
func TestPlaintextAndTLSSitesShareTheirNodesOwnRelayPort(t *testing.T) {
	c := startController(t)
	relayAddr := freeAddress(t)
	tokenA := c.createRelayNode("edge-a", relayAddr)
	tokenB := c.createNode("edge-b")

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "from origin") }))
	defer origin.Close()

	// RelayListen pins edge-a's relay bind to the exact address both sites below also listen on; see
	// TestSiteSharesItsNodesOwnRelayPort's comment on the same line for why this is needed.
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

	certServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer certServer.Close()
	certPEM, keyPEM := encodeTestCertificate(t, certServer.TLS.Certificates[0])

	secureSite := fmt.Sprintf(`{"id":"secure-web","name":"secure-web","config":{"nodes":["edge-a"],"hostnames":["secure.test"],"listenAddress":%q,"listenPort":%d,
		"tls":true,"certificateSecret":"cert","privateKeySecret":"key","upstreams":[{"url":%q}]}}`, host, relayPort, origin.URL)
	c.call(http.MethodPost, "/api/sites", secureSite, http.StatusCreated)
	c.call(http.MethodPut, "/api/sites/secure-web/secrets/cert", string(certPEM), http.StatusCreated)
	c.call(http.MethodPut, "/api/sites/secure-web/secrets/key", string(keyPEM), http.StatusCreated)
	c.call(http.MethodPost, "/api/sites/secure-web/start", "", http.StatusOK)

	relayedPort := freePort(t)
	relayedSite := fmt.Sprintf(`{"id":"relayed-web","name":"relayed-web","config":{"nodes":["edge-b"],"listenAddress":"127.0.0.1","listenPort":%d,
		"upstreams":[{"url":%q,"via":[{"node":"edge-a"}]}]}}`, relayedPort, origin.URL)
	c.call(http.MethodPost, "/api/sites", relayedSite, http.StatusCreated)
	c.call(http.MethodPost, "/api/sites/relayed-web/start", "", http.StatusOK)

	eventually(t, "all three sites are running and in sync", func() bool {
		return c.inSync("edge-a", "edge-b")
	})
	eventually(t, "edge-b's link to edge-a is up", func() bool { return linksUp(edgeB) })

	if body := getHost(t, "http://"+relayAddr+"/", "local.test"); body != "from origin" {
		t.Fatalf("plaintext site sharing the relay's own address: body = %q", body)
	}
	if body := getSNI(t, relayAddr, "secure.test"); body != "from origin" {
		t.Fatalf("TLS site sharing the relay's own address: body = %q", body)
	}
	target := fmt.Sprintf("http://127.0.0.1:%d/", relayedPort)
	eventually(t, "the relayed site reaches the origin through edge-a's shared address", func() bool {
		return probe(target) == "from origin"
	})
}

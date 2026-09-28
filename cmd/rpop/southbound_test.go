package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/agent"
	"github.com/rpop-project/rpop/internal/control"
	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/sharedport"
	"github.com/rpop-project/rpop/internal/southbound"
	"github.com/rpop-project/rpop/internal/store"
)

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

// consoleClient drives a running console (see startController) over real HTTP, the way an operator's browser
// would — unlike internal/agent's own test helper, which calls service.Handler() in-process, this one exercises
// the console listening through the shared-port registry (registry.PutPlaintextOwner) on a real TCP socket,
// possibly the same socket startSouthbound also serves southbound's TLS traffic on.
type consoleClient struct {
	t      *testing.T
	base   string
	client *http.Client
	cookie *http.Cookie
}

// startController starts a controller with the console listening at consoleAddr and southbound at
// southboundAddr — the same address for both (told apart by first byte: plaintext console, TLS southbound) or a
// distinct one (the regression case: southbound behaves the same whether or not it shares a port).
func startController(t *testing.T, consoleAddr, southboundAddr string) *consoleClient {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	service := control.New(store.New(db), zap.NewNop())
	service.SetEmbeddedNode(false)
	t.Cleanup(service.StopAll)

	registry := sharedport.NewRegistry()
	service.SetSharedPortRegistry(registry)
	if err := registry.PutPlaintextOwner(consoleAddr, service.Handler(), nil); err != nil {
		t.Fatalf("PutPlaintextOwner: %v", err)
	}
	t.Cleanup(func() { registry.RemovePlaintextOwner(consoleAddr) })

	ctx, cancel := context.WithCancel(context.Background())
	southbound := startSouthbound(ctx, zap.NewNop(), service, registry, southboundAddr, control.DefaultMaxConcurrentSouthboundStreamsPerConn)
	t.Cleanup(func() {
		cancel()
		_ = southbound.Close()
	})

	c := &consoleClient{t: t, base: "http://" + consoleAddr, client: &http.Client{Timeout: 5 * time.Second}}
	c.call(http.MethodPost, "/api/auth/setup", `{"password":"test-admin-password-2026"}`, http.StatusOK)
	return c
}

func (c *consoleClient) call(method, path, body string, want int) []byte {
	c.t.Helper()
	request, err := http.NewRequest(method, c.base+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if c.cookie != nil {
		request.AddCookie(c.cookie)
	}
	response, err := c.client.Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != want {
		c.t.Fatalf("%s %s = %d, want %d: %s", method, path, response.StatusCode, want, data)
	}
	for _, cookie := range response.Cookies() {
		c.cookie = cookie
	}
	return data
}

func (c *consoleClient) createRelayNode(id, relayAddress string) string {
	c.t.Helper()
	body := c.call(http.MethodPost, "/api/nodes", fmt.Sprintf(`{"id":%q,"name":"Node %s","relayAddress":%q}`, id, id, relayAddress), http.StatusCreated)
	var created struct {
		JoinToken string `json:"joinToken"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.JoinToken == "" {
		c.t.Fatalf("create node response %s: %v", body, err)
	}
	return created.JoinToken
}

type southboundTestNodeState struct {
	Registered bool `json:"registered"`
	Online     bool `json:"online"`
}

func (c *consoleClient) node(id string) southboundTestNodeState {
	c.t.Helper()
	var nodes []struct {
		ID string `json:"id"`
		southboundTestNodeState
	}
	if err := json.Unmarshal(c.call(http.MethodGet, "/api/nodes", "", http.StatusOK), &nodes); err != nil {
		c.t.Fatal(err)
	}
	for _, node := range nodes {
		if node.ID == id {
			return node.southboundTestNodeState
		}
	}
	c.t.Fatalf("node %q is not listed", id)
	return southboundTestNodeState{}
}

// TestSouthboundSharesAddressWithConsoleAndServesBothSNIPaths is stage two's southbound integration test: the
// console (plaintext) and southbound (TLS) share one address through internal/sharedport.Registry, and a real
// node completes the whole lifecycle — first registration, then steady-state watch — over the exact-SNI TLS
// path startSouthbound registers (registry.PutTLSOwner(addr, pki.ControllerName, ...)): every southbound call a
// current node makes (register, renew, watch, status) authenticates with a ServerName pinned to
// pki.ControllerName — pki.BootstrapClientConfig for the first, pki.Identity.ControllerClientConfig for the
// rest — and crypto/tls sends that as the ClientHello's SNI regardless of the -controller URL's own host (an
// explicit, non-empty, non-IP Config.ServerName is what crypto/tls's hostnameInSNI checks, not the address being
// dialed; see BootstrapClientConfig's doc comment), even though the -controller URL below is a literal IP
// address. The node reaching "registered" and then "online" (its watch stream connected) proves the exact-SNI
// path works end to end while sharing the address with the console.
//
// The default TLS owner (registry.PutDefaultTLSOwner) this same address also carries is not exercised by this
// test at all — a current node never sends an SNI it wouldn't match, so nothing here reaches it. Its own
// coverage is TestSouthboundBootstrapWithNoSNIReachesDefaultOwner, which dials with no SNI on purpose (what a
// node predating the SNI pin above still does, and what any client dialing without a ServerName set at all
// does).
func TestSouthboundSharesAddressWithConsoleAndServesBothSNIPaths(t *testing.T) {
	addr := freeAddress(t)
	testSouthboundRegistersAndWatches(t, addr, addr)
}

// TestSouthboundStillWorksOnItsOwnAddress is the regression case: southbound behaves the same when it does not
// share a port with the console (-southbound-addr distinct from -addr, the common production default).
func TestSouthboundStillWorksOnItsOwnAddress(t *testing.T) {
	testSouthboundRegistersAndWatches(t, freeAddress(t), freeAddress(t))
}

// TestSouthboundBootstrapWithNoSNIReachesDefaultOwner is the genuine counterpart to
// TestSouthboundSharesAddressWithConsoleAndServesBothSNIPaths's exact-SNI path: it dials southbound with no SNI
// at all — an explicitly empty tls.Config.ServerName, bypassing net/http's own "fill ServerName from the dial
// host when empty" behavior (see (*persistConn).addTLS) via a custom DialTLSContext — which is what a node whose
// binary predates pinning the bootstrap SNI to pki.ControllerName still sends (its BootstrapClientConfig never
// set ServerName, so net/http filled it from the -controller URL's own host, which crypto/tls then sends as SNI
// only when that host is not itself a literal IP address; see hostnameInSNI in the standard library and RFC 6066
// §3), and what any other client that never sets ServerName sends regardless of the URL it dials. The
// registration reaching the controller and getting back a signed certificate — over an address that also carries
// the exact-SNI owner (pki.ControllerName) and the plaintext console — proves dispatch.go's fallback order
// actually lands an unmatched SNI on registry.PutDefaultTLSOwner instead of failing the handshake.
func TestSouthboundBootstrapWithNoSNIReachesDefaultOwner(t *testing.T) {
	addr := freeAddress(t)
	c := startController(t, addr, addr)
	joinToken := c.createRelayNode("edge-1", "")

	token, err := pki.ParseJoinToken(joinToken)
	if err != nil {
		t.Fatal(err)
	}
	_, csrPEM, err := pki.NewKeyAndCSR(token.NodeID)
	if err != nil {
		t.Fatal(err)
	}

	transport := &http.Transport{
		// ForceAttemptHTTP2 is required alongside a custom DialTLSContext: net/http's Transport otherwise
		// stays conservative and leaves HTTP/2 off for a caller supplying its own TLS dialer (see
		// Transport.protocols), and southbound's handler is HTTP/2 only.
		ForceAttemptHTTP2: true,
		DialTLSContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			raw, err := (&net.Dialer{}).DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			conn := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, ServerName: "", NextProtos: []string{"h2", "http/1.1"}})
			if err := conn.HandshakeContext(ctx); err != nil {
				_ = raw.Close()
				return nil, err
			}
			return conn, nil
		},
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	t.Cleanup(transport.CloseIdleConnections)

	body, err := json.Marshal(southbound.RegisterRequest{Token: joinToken, CSRPEM: csrPEM})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "https://"+addr+southbound.RegisterPath, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(southbound.ProtocolVersionHeader, strconv.Itoa(southbound.ProtocolVersion))
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("register with no SNI: %v", err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("register with no SNI: status = %d, body = %s", response.StatusCode, data)
	}
	var decoded southbound.RegisterResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode register response: %v (%s)", err, data)
	}
	if decoded.CertificatePEM == "" || decoded.CAPEM == "" {
		t.Fatalf("register response missing certificate or CA: %+v", decoded)
	}
	certificate, err := pki.ParseCertificate(decoded.CertificatePEM)
	if err != nil {
		t.Fatalf("controller issued an unparsable certificate: %v", err)
	}
	if id, ok := pki.NodeIDFromCertificate(certificate); !ok || id != token.NodeID {
		t.Fatalf("certificate names node %q, want %q", id, token.NodeID)
	}
}

func testSouthboundRegistersAndWatches(t *testing.T, consoleAddr, southboundAddr string) {
	t.Helper()
	c := startController(t, consoleAddr, southboundAddr)
	token := c.createRelayNode("edge-1", "")

	node, err := agent.New(agent.Config{
		ControllerURL: "https://" + southboundAddr, JoinToken: token, DataDir: t.TempDir(), Version: "test",
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- node.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("node run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("node did not stop")
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		state := c.node("edge-1")
		if state.Registered && state.Online {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("node did not reach registered+online: %+v", state)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

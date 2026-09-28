package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/agent"
	"github.com/rpop-project/rpop/internal/control"
	"github.com/rpop-project/rpop/internal/sharedport"
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
// node completes the whole lifecycle — first registration, then steady-state watch — across both TLS paths
// startSouthbound registers:
//
//   - The node's -controller URL below is a literal IP address (127.0.0.1), so its first-ever registration
//     dials with no SNI at all (crypto/tls omits it for an IP host regardless of pki.BootstrapClientConfig's
//     ServerName; see that function's doc comment) — this must reach the default TLS owner
//     (registry.PutDefaultTLSOwner).
//   - Every call after that (renew, watch, status) authenticates with pki.Identity.ControllerClientConfig,
//     which pins ServerName to pki.ControllerName — this must reach the exact TLS owner
//     (registry.PutTLSOwner(addr, pki.ControllerName, ...)).
//
// The node reaching "registered" (through the first path) and "online" (its watch stream connected, through the
// second) proves both.
func TestSouthboundSharesAddressWithConsoleAndServesBothSNIPaths(t *testing.T) {
	addr := freeAddress(t)
	testSouthboundRegistersAndWatches(t, addr, addr)
}

// TestSouthboundStillWorksOnItsOwnAddress is the regression case: southbound behaves the same when it does not
// share a port with the console (-southbound-addr distinct from -addr, the common production default).
func TestSouthboundStillWorksOnItsOwnAddress(t *testing.T) {
	testSouthboundRegistersAndWatches(t, freeAddress(t), freeAddress(t))
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

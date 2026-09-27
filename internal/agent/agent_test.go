package agent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/control"
	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/southbound"
	"github.com/rpop-project/rpop/internal/store"
)

// controller is a controller with its console API and southbound listener, as a node sees it.
type controller struct {
	t          *testing.T
	service    *control.Control
	console    http.Handler
	cookie     *http.Cookie
	southbound *httptest.Server
}

func startController(t *testing.T) *controller {
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
	tlsConfig, err := service.SouthboundTLSConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(service.SouthboundHandler())
	server.TLS, server.EnableHTTP2 = tlsConfig, true
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	c := &controller{t: t, service: service, console: service.Handler(), southbound: server}
	setup := c.call(http.MethodPost, "/api/auth/setup", `{"password":"test-admin-password-2026"}`, http.StatusOK)
	c.cookie = setup.Result().Cookies()[0]
	return c
}

func (c *controller) call(method, path, body string, want int) *httptest.ResponseRecorder {
	c.t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if c.cookie != nil {
		request.AddCookie(c.cookie)
	}
	response := httptest.NewRecorder()
	c.console.ServeHTTP(response, request)
	if response.Code != want {
		c.t.Fatalf("%s %s = %d, want %d: %s", method, path, response.Code, want, response.Body.String())
	}
	return response
}

func (c *controller) createNode(id string) string {
	c.t.Helper()
	response := c.call(http.MethodPost, "/api/nodes", fmt.Sprintf(`{"id":%q,"name":"Node %s"}`, id, id), http.StatusCreated)
	var created struct {
		JoinToken string `json:"joinToken"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.JoinToken == "" {
		c.t.Fatalf("create node response %s: %v", response.Body.String(), err)
	}
	return created.JoinToken
}

type nodeState struct {
	ID              string   `json:"id"`
	Registered      bool     `json:"registered"`
	Online          bool     `json:"online"`
	InSync          bool     `json:"inSync"`
	AppliedRevision int64    `json:"appliedRevision"`
	Running         []string `json:"running"`
}

func (c *controller) node(id string) nodeState {
	c.t.Helper()
	var nodes []nodeState
	if err := json.Unmarshal(c.call(http.MethodGet, "/api/nodes", "", http.StatusOK).Body.Bytes(), &nodes); err != nil {
		c.t.Fatal(err)
	}
	for _, node := range nodes {
		if node.ID == id {
			return node
		}
	}
	c.t.Fatalf("node %q is not listed", id)
	return nodeState{}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func startAgent(t *testing.T, cfg Config) (*Agent, func()) {
	t.Helper()
	node, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- node.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
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
	}
	t.Cleanup(stop)
	return node, stop
}

func get(t *testing.T, target string) string {
	t.Helper()
	response, err := http.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return string(body)
}

func TestNodeFollowsControllerAndServesCacheWhenItIsGone(t *testing.T) {
	c := startController(t)
	token := c.createNode("edge-1")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "from origin") }))
	defer origin.Close()
	dataDir := t.TempDir()
	node, stop := startAgent(t, Config{ControllerURL: c.southbound.URL, JoinToken: token, DataDir: dataDir, Version: "test"})
	eventually(t, "the node registers and connects", func() bool { return c.node("edge-1").Online })

	port := freePort(t)
	site := fmt.Sprintf(`{"id":"web","name":"web","config":{"nodes":["edge-1"],"listenAddress":"127.0.0.1","listenPort":%d,"upstreams":[{"url":%q}]}}`, port, origin.URL)
	c.call(http.MethodPost, "/api/sites", site, http.StatusCreated)
	c.call(http.MethodPost, "/api/sites/web/start", "", http.StatusOK)
	eventually(t, "the node serves the site", func() bool { return node.Engine().Running("web") })
	if body := get(t, fmt.Sprintf("http://127.0.0.1:%d/", port)); body != "from origin" {
		t.Fatalf("proxied body = %q", body)
	}
	eventually(t, "the node reports it applied the snapshot", func() bool {
		state := c.node("edge-1")
		return state.Registered && state.InSync && len(state.Running) == 1 && state.Running[0] == "web"
	})
	for _, name := range []string{keyFile, certFile, caFile, snapshotFile} {
		info, err := os.Stat(filepath.Join(dataDir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: mode %v, %v", name, info.Mode(), err)
		}
	}

	stop()
	c.southbound.Close()
	restarted, _ := startAgent(t, Config{ControllerURL: c.southbound.URL, DataDir: dataDir, Version: "test"})
	eventually(t, "the restarted node serves its cached snapshot", func() bool { return restarted.Engine().Running("web") })
	if body := get(t, fmt.Sprintf("http://127.0.0.1:%d/", port)); body != "from origin" {
		t.Fatalf("proxied body from cache = %q", body)
	}
}

func TestStoppingASiteRemovesItFromTheNode(t *testing.T) {
	c := startController(t)
	token := c.createNode("edge-1")
	port := freePort(t)
	site := fmt.Sprintf(`{"id":"web","name":"web","config":{"nodes":["edge-1"],"listenAddress":"127.0.0.1","listenPort":%d,"upstreams":[{"url":"http://127.0.0.1:9"}]}}`, port)
	c.call(http.MethodPost, "/api/sites", site, http.StatusCreated)
	c.call(http.MethodPost, "/api/sites/web/start", "", http.StatusOK)
	node, _ := startAgent(t, Config{ControllerURL: c.southbound.URL, JoinToken: token, DataDir: t.TempDir()})
	eventually(t, "the node serves the site", func() bool { return node.Engine().Running("web") })
	c.call(http.MethodPost, "/api/sites/web/stop", "", http.StatusOK)
	eventually(t, "the node stops the site", func() bool { return !node.Engine().Running("web") })
	c.call(http.MethodDelete, "/api/nodes/edge-1", "", http.StatusConflict)
}

func TestRegistrationIsSingleUseAndRevokesEarlierCertificates(t *testing.T) {
	c := startController(t)
	token := c.createNode("edge-1")
	base, _ := url.Parse(c.southbound.URL)
	ctx := context.Background()
	first, err := register(ctx, base, t.TempDir(), token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := register(ctx, base, t.TempDir(), token); !isUnauthorized(err) {
		t.Fatalf("reused join token: %v", err)
	}
	firstClient := newClient(first.ControllerClientConfig())
	report := func(client *http.Client) error {
		return postJSON(ctx, client, base.JoinPath(southbound.StatusPath).String(), southbound.Status{Version: "test"}, nil)
	}
	if err := report(firstClient); err != nil {
		t.Fatalf("status with the registered certificate: %v", err)
	}

	var reissued struct {
		JoinToken string `json:"joinToken"`
	}
	_ = json.Unmarshal(c.call(http.MethodPost, "/api/nodes/edge-1/token", "", http.StatusOK).Body.Bytes(), &reissued)
	second, err := register(ctx, base, t.TempDir(), reissued.JoinToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := report(newClient(second.ControllerClientConfig())); err != nil {
		t.Fatalf("status with the new certificate: %v", err)
	}
	firstClient.CloseIdleConnections()
	if err := report(firstClient); !isUnauthorized(err) {
		t.Fatalf("status with a revoked certificate: %v", err)
	}
}

func TestRegistrationFailuresAreRateLimited(t *testing.T) {
	c := startController(t)
	token := c.createNode("edge-1")
	base, _ := url.Parse(c.southbound.URL)
	forged := token[:strings.LastIndex(token, ".")-1] + "x" + token[strings.LastIndex(token, "."):]
	var last error
	for range 11 {
		_, last = register(context.Background(), base, t.TempDir(), forged)
	}
	var status *statusError
	if !errors.As(last, &status) || status.code != http.StatusTooManyRequests {
		t.Fatalf("eleventh failed registration: %v", last)
	}
	if _, err := register(context.Background(), base, t.TempDir(), token); err == nil {
		t.Fatal("a rate-limited address could still register")
	}
}

func TestWatchRejectsClientsWithoutCertificates(t *testing.T) {
	c := startController(t)
	token, err := pki.ParseJoinToken(c.createNode("edge-1"))
	if err != nil {
		t.Fatal(err)
	}
	client := newClient(pki.BootstrapClientConfig(token.CAFingerprint))
	defer client.CloseIdleConnections()
	response, err := client.Get(c.southbound.URL + southbound.WatchPath)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("anonymous watch = %d: %s", response.StatusCode, bytes.TrimSpace(body))
	}
}

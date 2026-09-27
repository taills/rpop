package agent

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// connectProxy is an HTTP CONNECT proxy that counts the tunnels it opens, which shows whether traffic took a path
// ending in it.
type connectProxy struct {
	listener net.Listener
	tunnels  atomic.Int32
}

func startConnectProxy(t *testing.T) *connectProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	p := &connectProxy{listener: listener}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go p.serve(conn)
		}
	}()
	return p
}

func (p *connectProxy) serve(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	request, err := http.ReadRequest(reader)
	if err != nil || request.Method != http.MethodConnect {
		return
	}
	upstream, err := net.Dial("tcp", request.Host)
	if err != nil {
		io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer upstream.Close()
	p.tunnels.Add(1)
	io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
	go func() {
		io.Copy(upstream, reader)
		upstream.(*net.TCPConn).CloseWrite()
	}()
	io.Copy(conn, upstream)
}

func (p *connectProxy) address() string { return p.listener.Addr().String() }

func freeAddress(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("127.0.0.1:%d", freePort(t))
}

// probe fetches a URL and returns its body, or "" when the request fails.
func probe(target string) string {
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(target)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		return ""
	}
	return string(body)
}

// linksUp reports whether a node has overlay links and each has a connection.
func linksUp(a *Agent) bool {
	a.mu.Lock()
	o := a.overlay
	a.mu.Unlock()
	if o == nil {
		return false
	}
	links := o.Links()
	for _, link := range links {
		if link.Connections == 0 {
			return false
		}
	}
	return len(links) > 0
}

func (c *controller) inSync(ids ...string) bool {
	for _, id := range ids {
		if state := c.node(id); !state.Online || !state.InSync {
			return false
		}
	}
	return true
}

func TestTrafficCrossesNodesAndFailsOverToBackupPaths(t *testing.T) {
	c := startController(t)
	relay2, relay3 := freeAddress(t), freeAddress(t)
	tokens := map[string]string{"edge-1": c.createNode("edge-1"), "edge-2": c.createRelayNode("edge-2", relay2), "edge-3": c.createRelayNode("edge-3", relay3)}
	egress := startConnectProxy(t)
	c.call(http.MethodPost, "/api/proxies", fmt.Sprintf(`{"id":"exit-proxy","type":"http","address":%q}`, egress.address()), http.StatusCreated)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "from origin") }))
	defer origin.Close()

	nodes := map[string]*Agent{}
	stops := map[string]func(){}
	dataDirs := map[string]string{}
	for id, token := range tokens {
		dataDirs[id] = t.TempDir()
		nodes[id], stops[id] = startAgent(t, Config{ControllerURL: c.southbound.URL, JoinToken: token, DataDir: dataDirs[id], Version: "test"})
	}
	eventually(t, "every node registers", func() bool { return c.inSync("edge-1", "edge-2", "edge-3") })

	port := freePort(t)
	site := fmt.Sprintf(`{"id":"web","name":"web","config":{"nodes":["edge-1"],"listenAddress":"127.0.0.1","listenPort":%d,
		"upstreams":[{"url":%q,"paths":[{"via":[{"node":"edge-2"},{"node":"edge-3"},{"proxy":"exit-proxy"}]},{"via":[]}]}]}}`, port, origin.URL)
	c.call(http.MethodPost, "/api/sites", site, http.StatusCreated)
	c.call(http.MethodPost, "/api/sites/web/start", "", http.StatusOK)
	eventually(t, "the ingress serves the site and the links along the path are up", func() bool {
		return nodes["edge-1"].Engine().Running("web") && c.inSync("edge-1", "edge-2", "edge-3") &&
			linksUp(nodes["edge-1"]) && linksUp(nodes["edge-2"])
	})
	target := fmt.Sprintf("http://127.0.0.1:%d/", port)
	if body := get(t, target); body != "from origin" {
		t.Fatalf("body = %q", body)
	}
	if got := egress.tunnels.Load(); got != 1 {
		t.Fatalf("the exit's egress proxy opened %d tunnels, want 1: the request did not take edge-1 > edge-2 > edge-3", got)
	}

	stops["edge-3"]()
	eventually(t, "requests fail over to the direct path", func() bool { return probe(target) == "from origin" })
	for range 5 {
		if body := probe(target); body != "from origin" {
			t.Fatalf("a request after failing over got %q", body)
		}
	}
	if got := egress.tunnels.Load(); got != 1 {
		t.Fatalf("requests still reached the egress proxy (%d tunnels) while edge-3 was down", got)
	}

	// Once edge-3 is back, the preferred path serves again after its cooldown.
	startAgent(t, Config{ControllerURL: c.southbound.URL, DataDir: dataDirs["edge-3"], Version: "test"})
	eventuallyWithin(t, 45*time.Second, "requests fail back to the preferred path", func() bool {
		return probe(target) == "from origin" && egress.tunnels.Load() > 1
	})
}

func TestEmbeddedNodeRoutesThroughRemoteNodes(t *testing.T) {
	c := startController(t)
	c.service.SetEmbeddedNode(true)
	token := c.createRelayNode("edge-2", freeAddress(t))
	egress := startConnectProxy(t)
	c.call(http.MethodPost, "/api/proxies", fmt.Sprintf(`{"id":"exit-proxy","type":"http","address":%q}`, egress.address()), http.StatusCreated)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "from origin")
	}))
	defer origin.Close()
	startAgent(t, Config{ControllerURL: c.southbound.URL, JoinToken: token, DataDir: t.TempDir(), Version: "test"})
	eventually(t, "the relay registers", func() bool { return c.inSync("edge-2") })

	port := freePort(t)
	site := fmt.Sprintf(`{"id":"web","name":"web","config":{"listenAddress":"127.0.0.1","listenPort":%d,
		"upstreams":[{"url":%q,"via":[{"node":"edge-2"},{"proxy":"exit-proxy"}]}]}}`, port, origin.URL)
	c.call(http.MethodPost, "/api/sites", site, http.StatusCreated)
	c.call(http.MethodPost, "/api/sites/web/start", "", http.StatusOK)
	eventually(t, "the relay has its route and the embedded node's link to it is up", func() bool {
		links := c.node("local").Links
		return c.inSync("edge-2") && len(links) == 1 && links[0].Peer == "edge-2" && links[0].Connections > 0
	})
	if body := get(t, fmt.Sprintf("http://127.0.0.1:%d/", port)); body != "from origin" {
		t.Fatalf("body = %q", body)
	}
	if got := egress.tunnels.Load(); got != 1 {
		t.Fatalf("the egress proxy opened %d tunnels, want 1", got)
	}
}

package control

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/store"
)

func newPublishingControl(t *testing.T) (*Control, *store.Store, string) {
	t.Helper()
	s, _ := openSystemCATestStore(t)
	logDir := t.TempDir()
	c, err := NewWithLogDir(s, zap.NewNop(), logDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.StopAll()
		_ = c.CloseAccessLogs(context.Background())
	})
	return c, s, logDir
}

func siteIDs(c *Control, nodeID string) []string {
	s, ok := c.published.Snapshot(nodeID)
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(s.Sites))
	for _, site := range s.Sites {
		ids = append(ids, site.ID)
	}
	return ids
}

func saveNodes(t *testing.T, s *store.Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := s.SaveNode(context.Background(), store.Node{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPublishPlacesSitesOnTheirNodes(t *testing.T) {
	c, s, _ := newPublishingControl(t)
	saveNodes(t, s, "edge-1", "edge-2")
	upstream := textServer(t, "ok")
	sites := []store.Site{
		{ID: "embedded", Name: "embedded", Config: store.Config{ListenAddress: "127.0.0.1", ListenPort: freeLoopbackPort(t), Upstreams: []store.Upstream{{URL: upstream}}}},
		{ID: "edge", Name: "edge", Config: store.Config{Nodes: []string{"edge-1", "edge-2"}, ListenAddress: "0.0.0.0", ListenPort: 443, Upstreams: []store.Upstream{{URL: upstream}}}},
	}
	for _, site := range sites {
		if err := s.Save(context.Background(), site); err != nil {
			t.Fatal(err)
		}
		if err := c.start(context.Background(), site.ID); err != nil {
			t.Fatalf("start %s: %v", site.ID, err)
		}
	}
	if got := strings.Join(siteIDs(c, LocalNodeID), ","); got != "embedded" {
		t.Fatalf("local snapshot sites = %q", got)
	}
	for _, node := range []string{"edge-1", "edge-2"} {
		if got := strings.Join(siteIDs(c, node), ","); got != "edge" {
			t.Fatalf("%s snapshot sites = %q", node, got)
		}
	}
	if c.engine.Running("edge") {
		t.Fatal("a site placed on remote nodes was started on the embedded node")
	}
	if !c.siteRunning(sites[1]) {
		t.Fatal("a published remote site is not reported as running")
	}
	edge, _ := c.published.Snapshot("edge-1")
	if edge.Revision != c.published.Revision() || edge.NodeID != "edge-1" {
		t.Fatalf("snapshot header = revision %d node %q", edge.Revision, edge.NodeID)
	}
}

func TestPublishSkipsUnknownAndEmbeddedNodesItDoesNotRun(t *testing.T) {
	c, s, _ := newPublishingControl(t)
	saveNodes(t, s, "edge-1")
	c.SetEmbeddedNode(false)
	upstream := textServer(t, "ok")
	site := store.Site{ID: "legacy", Name: "legacy", Config: store.Config{ListenAddress: "127.0.0.1", ListenPort: freeLoopbackPort(t), Upstreams: []store.Upstream{{URL: upstream}}}}
	if err := s.Save(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if err := c.start(context.Background(), "legacy"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.published.Snapshot(LocalNodeID); ok || c.engine.Running("legacy") {
		t.Fatal("a controller without an embedded node published or ran a local snapshot")
	}
	if edge, ok := c.published.Snapshot("edge-1"); !ok || len(edge.Sites) != 0 {
		t.Fatalf("registered node snapshot = %#v, %v", edge, ok)
	}
	if err := c.validateNodeReferences(context.Background(), site); err == nil {
		t.Fatal("placement on the missing embedded node was accepted")
	}
	site.Config.Nodes = []string{"edge-9"}
	if err := c.validateNodeReferences(context.Background(), site); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("placement on an unknown node error = %v", err)
	}
	site.Config.Nodes = []string{"edge-1"}
	if err := c.validateNodeReferences(context.Background(), site); err != nil {
		t.Fatal(err)
	}
}

func TestPublishRevisionIsMonotonicAcrossRestarts(t *testing.T) {
	c, s, logDir := newPublishingControl(t)
	site := store.Site{ID: "a", Name: "a", Config: store.Config{ListenAddress: "127.0.0.1", ListenPort: freeLoopbackPort(t), Upstreams: []store.Upstream{{URL: textServer(t, "ok")}}}}
	if err := s.Save(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if err := c.start(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	first := c.published.Revision()
	if err := c.stop(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if c.published.Revision() <= first {
		t.Fatalf("revision did not advance: %d -> %d", first, c.published.Revision())
	}
	last := c.published.Revision()
	restarted, err := NewWithLogDir(s, zap.NewNop(), logDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.CloseAccessLogs(context.Background())
	if restarted.published.Revision() != last {
		t.Fatalf("revision after restart = %d, want %d", restarted.published.Revision(), last)
	}
}

func TestPublishKeepsLastGoodSpecWhenResolutionFails(t *testing.T) {
	c, s, _ := newPublishingControl(t)
	saveNodes(t, s, "edge-1")
	site := store.Site{ID: "edge", Name: "edge", Config: store.Config{Nodes: []string{"edge-1"}, ListenAddress: "0.0.0.0", ListenPort: 8443, TLS: true, CertificateSecret: "cert", PrivateKeySecret: "key", Upstreams: []store.Upstream{{URL: "http://origin.test"}}}}
	if err := s.Save(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	material := makeTestTLSMaterial(t)
	if err := s.SaveSecret(context.Background(), "edge", "cert", material.serverPEM); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSecret(context.Background(), "edge", "key", material.serverKeyPEM); err != nil {
		t.Fatal(err)
	}
	if err := c.start(context.Background(), "edge"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSecret(context.Background(), "edge", "key"); err != nil {
		t.Fatal(err)
	}
	c.opMu.Lock()
	errs := c.publishLocked(context.Background(), publishScope{all: true})
	c.opMu.Unlock()
	if errs["edge"] == nil {
		t.Fatal("unresolvable site reported no error")
	}
	edge, _ := c.published.Snapshot("edge-1")
	if len(edge.Sites) != 1 || edge.Sites[0].Certificate == nil || edge.Sites[0].Certificate.PrivateKeyPEM != string(material.serverKeyPEM) {
		t.Fatalf("remote node lost the last good spec: %#v", edge.Sites)
	}
}

func TestValidateRejectsInvalidPlacement(t *testing.T) {
	for _, nodes := range [][]string{{"Edge"}, {"edge_1"}, {"-edge"}, {"a", "a"}} {
		site := store.Site{ID: "s", Name: "s", Config: store.Config{Nodes: nodes, ListenPort: 80, Upstreams: []store.Upstream{{URL: "http://a"}}}}
		if err := validate(site); err == nil {
			t.Fatalf("placement %v was accepted", nodes)
		}
	}
}

func textServer(t *testing.T, body string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
	t.Cleanup(server.Close)
	return server.URL
}

func TestStartingOneSiteResolvesOnlyThatSite(t *testing.T) {
	c, s, _ := newPublishingControl(t)
	saveNodes(t, s, "edge-1")
	material := makeTestTLSMaterial(t)
	for _, id := range []string{"a", "b"} {
		site := store.Site{ID: id, Name: id, Config: store.Config{Nodes: []string{"edge-1"}, ListenAddress: "0.0.0.0", ListenPort: 8443, TLS: true,
			Hostnames: []string{id + ".example.test"}, CertificateSecret: "cert", PrivateKeySecret: "key", Upstreams: []store.Upstream{{URL: "http://origin.test"}}}}
		if err := s.Save(context.Background(), site); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveSecret(context.Background(), id, "cert", material.serverPEM); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveSecret(context.Background(), id, "key", material.serverKeyPEM); err != nil {
			t.Fatal(err)
		}
		if err := c.start(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteSecret(context.Background(), "b", "key"); err != nil {
		t.Fatal(err)
	}
	if err := c.start(context.Background(), "a"); err != nil {
		t.Fatalf("reloading a failed because of b: %v", err)
	}
	if got := strings.Join(siteIDs(c, "edge-1"), ","); got != "a,b" {
		t.Fatalf("edge-1 sites after reloading a = %q", got)
	}
	c.opMu.Lock()
	errs := c.publishLocked(context.Background(), publishScope{all: true})
	c.opMu.Unlock()
	if errs["b"] == nil || errs["a"] != nil {
		t.Fatalf("full publication errors = %v", errs)
	}
	if got := strings.Join(siteIDs(c, "edge-1"), ","); got != "a,b" {
		t.Fatalf("edge-1 sites after a failed resolution of b = %q", got)
	}
}

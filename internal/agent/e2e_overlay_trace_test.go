package agent

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rpop-project/rpop/internal/overlay"
)

// TestLogPipelineTunnelTimelineAcrossEntryRelayAndExit covers D22's full-path timeline end to end: a request
// whose upstream path crosses three real, separately registered nodes (an entry that hosts the site, a relay,
// and an exit that dials the origin directly) must produce one access log record tying its track id to a
// tunnel id, and that tunnel id's timeline must carry arrived/established/ended events from every one of the
// three hops, in order.
func TestLogPipelineTunnelTimelineAcrossEntryRelayAndExit(t *testing.T) {
	c := startLoggingController(t, nil)
	entryToken := c.createNode("edge-1")
	relayToken := c.createRelayNode("edge-2", freeAddress(t))
	exitToken := c.createRelayNode("edge-3", freeAddress(t))
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "from origin") }))
	defer origin.Close()

	entry, _ := startAgent(t, Config{ControllerURL: c.southbound.URL, JoinToken: entryToken, DataDir: t.TempDir(), Version: "test"})
	relay, _ := startAgent(t, Config{ControllerURL: c.southbound.URL, JoinToken: relayToken, DataDir: t.TempDir(), Version: "test"})
	exit, _ := startAgent(t, Config{ControllerURL: c.southbound.URL, JoinToken: exitToken, DataDir: t.TempDir(), Version: "test"})
	eventually(t, "every node registers", func() bool { return c.inSync("edge-1", "edge-2", "edge-3") })

	port := freePort(t)
	site := fmt.Sprintf(`{"id":"web","name":"web","config":{"nodes":["edge-1"],"listenAddress":"127.0.0.1","listenPort":%d,
		"upstreams":[{"url":%q,"paths":[{"via":[{"node":"edge-2"},{"node":"edge-3"}]}]}],"accessLog":{"adapterId":"default"}}}`, port, origin.URL)
	c.call(http.MethodPost, "/api/sites", site, http.StatusCreated)
	c.call(http.MethodPost, "/api/sites/web/start", "", http.StatusOK)
	eventually(t, "the path across every hop is up", func() bool {
		return entry.Engine().Running("web") && c.inSync("edge-1", "edge-2", "edge-3") && linksUp(entry) && linksUp(relay)
	})

	target := fmt.Sprintf("http://127.0.0.1:%d/", port)
	if body := get(t, target); body != "from origin" {
		t.Fatalf("body = %q", body)
	}

	var tunnelID string
	waitForAck(t, entry, 1)
	eventuallyWithin(t, 10*time.Second, "the access log record carries a tunnel id", func() bool {
		result := c.searchSite("web")
		if result.Total != 1 || result.Records[0].TunnelID == "" {
			return false
		}
		tunnelID = result.Records[0].TunnelID
		return true
	})

	// Stopping the site closes its pooled connections, which is what makes every hop report its tunnel's ended
	// event: production connections are kept warm indefinitely (P6) so a single request would otherwise never
	// see one. This mirrors internal/dataplane's own TestRequestThroughOverlayCarriesOneTrackAndTunnelIDAcrossEveryHop,
	// which closes the same pooled transports directly; here a site reload/stop through the real API does it.
	c.call(http.MethodPost, "/api/sites/web/stop", "", http.StatusOK)

	var events []overlay.TunnelEvent
	eventuallyWithin(t, 20*time.Second, "the full tunnel timeline reaches the controller", func() bool {
		flush(entry)
		flush(relay)
		flush(exit)
		events = c.tunnelEvents(tunnelID)
		return len(events) == 9
	})

	wantRoles := map[string]string{"edge-1": overlay.RoleEntry, "edge-2": overlay.RoleRelay, "edge-3": overlay.RoleExit}
	stagesSeen := map[string]map[string]bool{"edge-1": {}, "edge-2": {}, "edge-3": {}}
	for _, e := range events {
		if e.TunnelID != tunnelID {
			t.Fatalf("event %#v does not carry the request's tunnel id %q", e, tunnelID)
		}
		if e.Role != wantRoles[e.NodeID] {
			t.Fatalf("event %#v has role %q, want %q for node %s", e, e.Role, wantRoles[e.NodeID], e.NodeID)
		}
		stagesSeen[e.NodeID][e.Stage] = true
	}
	for node, stages := range stagesSeen {
		for _, stage := range []string{overlay.StageArrived, overlay.StageEstablished, overlay.StageEnded} {
			if !stages[stage] {
				t.Fatalf("node %s is missing a %s event; events = %#v", node, stage, events)
			}
		}
	}
	for i := 1; i < len(events); i++ {
		if events[i].Timestamp.Before(events[i-1].Timestamp) {
			t.Fatalf("events are not sorted by timestamp: %#v", events)
		}
	}
	if events[0].NodeID != "edge-1" || events[0].Stage != overlay.StageArrived {
		t.Fatalf("first event = %#v, want edge-1's arrived (the request's entry hop starts the timeline)", events[0])
	}
}

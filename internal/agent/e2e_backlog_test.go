package agent

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestLogPipelineBacklogsWhileTheControllerIsUnreachableAndRecoversWithoutLossOrDuplication covers D25/D24 end
// to end: while the controller cannot be reached at all, a node keeps forwarding and spooling instead of
// blocking; once it is reachable again, every spooled segment uploads and the controller ends up with exactly
// the records the node ever produced - no fewer (nothing lost to the outage) and no more (no duplicate from a
// retried upload).
func TestLogPipelineBacklogsWhileTheControllerIsUnreachableAndRecoversWithoutLossOrDuplication(t *testing.T) {
	var gate *southboundOutageGate
	c := startLoggingController(t, func(h http.Handler) http.Handler {
		gate = newSouthboundOutageGate(h)
		return gate
	})
	token := c.createNode("edge-1")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer origin.Close()
	node, _ := startAgent(t, Config{ControllerURL: c.southbound.URL, JoinToken: token, DataDir: t.TempDir(), Version: "test"})
	eventually(t, "the node registers", func() bool { return c.node("edge-1").Online })
	port := freePort(t)
	c.createSite("web", "edge-1", "default", origin.URL, port)
	eventually(t, "the node serves the site", func() bool { return node.Engine().Running("web") })
	target := fmt.Sprintf("http://127.0.0.1:%d/", port)

	const before, duringOutage = 3, 25
	for range before {
		if body := get(t, target); body != "ok" {
			t.Fatalf("body = %q", body)
		}
	}
	waitForAck(t, node, 1)

	gate.close()
	for i := range duringOutage {
		if body := get(t, target); body != "ok" {
			t.Fatalf("body = %q", body)
		}
		if i%5 == 4 {
			flush(node)
		}
	}
	eventuallyWithin(t, 5*time.Second, "the spool backlog grows while the controller is unreachable", func() bool {
		flush(node)
		return node.spool.Stats().PendingSegments > 1 && node.uploader.LastError() != ""
	})

	gate.reopen()
	eventuallyWithin(t, 30*time.Second, "every spooled segment uploads once the controller is reachable again", func() bool {
		flush(node)
		return node.spool.Stats().PendingSegments == 0
	})

	const total = before + duringOutage
	result := c.searchSite("web")
	if result.Total != total {
		t.Fatalf("access log total = %d, want exactly %d (no loss, no duplication)", result.Total, total)
	}
	seen := make(map[string]bool, result.Total)
	for _, record := range result.Records {
		if record.TrackID == "" {
			t.Fatalf("record without a track id: %#v", record)
		}
		if seen[record.TrackID] {
			t.Fatalf("duplicate record for track id %s", record.TrackID)
		}
		seen[record.TrackID] = true
	}
}

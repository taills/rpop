package agent

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestLogPipelineDropsOldestSegmentsUnderQuotaAndTheControllerAcceptsTheGapOnRecovery covers D25 end to end: a
// node with a tiny spool quota, cut off from the controller, must drop its oldest unacknowledged segments
// (never stall forwarding for it) and count the drop; once the controller is reachable again, it must accept
// the resulting gap in segment numbers as a legitimate skip-ahead (D24's "> HWM+1 is a legitimate jump, not an
// error") rather than getting stuck.
func TestLogPipelineDropsOldestSegmentsUnderQuotaAndTheControllerAcceptsTheGapOnRecovery(t *testing.T) {
	var gate *southboundOutageGate
	c := startLoggingController(t, func(h http.Handler) http.Handler {
		gate = newSouthboundOutageGate(h)
		return gate
	})
	token := c.createNode("edge-1")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer origin.Close()
	// A tiny quota (a handful of segments' worth of small access log records) so a modest backlog is enough to
	// force evictions without the test needing to generate megabytes of traffic.
	const quotaBytes = 4 << 10
	node, _ := startAgent(t, Config{
		ControllerURL: c.southbound.URL, JoinToken: token, DataDir: t.TempDir(), Version: "test",
		LogSpoolQuotaBytes: quotaBytes,
	})
	eventually(t, "the node registers", func() bool { return c.node("edge-1").Online })
	port := freePort(t)
	c.createSite("web", "edge-1", "default", origin.URL, port)
	eventually(t, "the node serves the site", func() bool { return node.Engine().Running("web") })
	target := fmt.Sprintf("http://127.0.0.1:%d/", port)

	// The controller has never seen this node's logs (HWM 0) before the outage even starts.
	gate.close()
	eventuallyWithin(t, 30*time.Second, "the quota evicts at least one unacknowledged segment", func() bool {
		for range 10 {
			if body := get(t, target); body != "ok" {
				t.Fatalf("body = %q", body)
			}
		}
		flush(node)
		return node.spool.Stats().QuotaDroppedSegments > 0
	})
	dropped := node.spool.Stats().QuotaDroppedSegments

	gate.reopen()
	eventuallyWithin(t, 30*time.Second, "the node uploads what survived the quota and the controller accepts the gap", func() bool {
		flush(node)
		return node.spool.Stats().PendingSegments == 0
	})
	if lastErr := node.uploader.LastError(); lastErr != "" {
		t.Fatalf("uploader still reports an error after recovering: %q", lastErr)
	}

	// statusLoop's periodic report (agent.go's statusInterval, 15s) is what carries LogStats to the controller.
	eventuallyWithin(t, 20*time.Second, "the controller's node view reports the quota drop and the advanced acked segment", func() bool {
		stats := c.nodeLogs("edge-1")
		return stats != nil && stats.QuotaDroppedSegments >= dropped && stats.AckedSegment > 0
	})
}

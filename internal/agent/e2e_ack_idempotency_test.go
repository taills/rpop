package agent

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLogPipelineRetriesIdempotentlyWhenAnAcknowledgmentIsLost covers D24's core idempotency guarantee end to
// end: the controller writes the record and persists the node's high-water mark before it ever acknowledges a
// segment, so if the acknowledgment itself is lost in transit, the node's retransmission of the very same
// segment must be answered without writing the record again.
func TestLogPipelineRetriesIdempotentlyWhenAnAcknowledgmentIsLost(t *testing.T) {
	var gate *ackDropGate
	c := startLoggingController(t, func(h http.Handler) http.Handler {
		gate = newAckDropGate(h)
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

	// The spool is brand new, so its very first segment is numbered 1: arrange for that segment's first upload
	// attempt to be written and acked by the controller, but answered to the node as a failure.
	gate.dropNextAckFor(1)
	target := fmt.Sprintf("http://127.0.0.1:%d/", port)
	if body := get(t, target); body != "ok" {
		t.Fatalf("body = %q", body)
	}

	waitForAck(t, node, 1)
	if lastErr := node.uploader.LastError(); lastErr != "" {
		t.Fatalf("uploader still reports an error after the retried segment succeeded: %q", lastErr)
	}

	result := c.searchSite("web")
	if result.Total != 1 {
		t.Fatalf("access log total = %d, want exactly 1 (the retried segment must not be written twice)", result.Total)
	}
	if acked := node.spool.AckedUpTo(); acked < 1 {
		t.Fatalf("node's locally recorded AckedUpTo = %d, want >= 1", acked)
	}
}

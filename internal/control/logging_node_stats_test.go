package control

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rpop-project/rpop/internal/southbound"
)

// TestNodesAPIExposesLogStatsAndWarnsOnRegressions covers item 6 of stage 5 step 3: Status.Logs is surfaced on
// the node view, and a new increase in a node's drop counters produces one rate-limited warning (D25).
func TestNodesAPIExposesLogStatsAndWarnsOnRegressions(t *testing.T) {
	h := newIngestHarness(t)
	token := h.createNode("edge-1")
	identity := h.register(token)
	client := nodeClient(identity)

	postStatus := func(status southbound.Status) {
		t.Helper()
		body, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Post(h.southbound.URL+southbound.StatusPath, "application/json", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("status upload = %d", response.StatusCode)
		}
	}
	nodeLogs := func() *southbound.LogStats {
		t.Helper()
		var nodes []nodeView
		if err := json.Unmarshal(h.call(http.MethodGet, "/api/nodes", "", http.StatusOK).Body.Bytes(), &nodes); err != nil {
			t.Fatal(err)
		}
		for _, node := range nodes {
			if node.ID == "edge-1" {
				return node.Logs
			}
		}
		t.Fatal("edge-1 is not listed")
		return nil
	}
	countWarnings := func(substr string) int {
		count := 0
		for _, entry := range h.logs.All() {
			if strings.Contains(entry.Message, substr) {
				count++
			}
		}
		return count
	}

	postStatus(southbound.Status{Logs: &southbound.LogStats{AckedSegment: 1}})
	if logs := nodeLogs(); logs == nil || logs.AckedSegment != 1 {
		t.Fatalf("node view logs = %#v, want AckedSegment=1", logs)
	}
	if n := countWarnings("dropped spooled log segments"); n != 0 {
		t.Fatalf("unexpected quota warning before any drop: %d", n)
	}

	h.logs.TakeAll()
	postStatus(southbound.Status{Logs: &southbound.LogStats{AckedSegment: 2, QuotaDroppedSegments: 3, QuotaDroppedBytes: 900}})
	if n := countWarnings("dropped spooled log segments"); n != 1 {
		t.Fatalf("quota warnings after the first increase = %d, want 1", n)
	}

	// Reporting the same counters again (no new drops) must not warn a second time (D25's "rate-limited").
	h.logs.TakeAll()
	postStatus(southbound.Status{Logs: &southbound.LogStats{AckedSegment: 3, QuotaDroppedSegments: 3, QuotaDroppedBytes: 900}})
	if n := countWarnings("dropped spooled log segments"); n != 0 {
		t.Fatalf("quota warnings when the counter did not move = %d, want 0", n)
	}

	h.logs.TakeAll()
	postStatus(southbound.Status{Logs: &southbound.LogStats{AckedSegment: 4, QuotaDroppedSegments: 3, AccessLogQueueDropped: 5}})
	if n := countWarnings("dropped log records from a full bounded ingest queue"); n != 1 {
		t.Fatalf("queue-drop warnings = %d, want 1", n)
	}
	if logs := nodeLogs(); logs == nil || logs.AccessLogQueueDropped != 5 || logs.AckedSegment != 4 {
		t.Fatalf("node view logs after the queue drop = %#v", logs)
	}
}

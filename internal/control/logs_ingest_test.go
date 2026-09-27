package control

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"

	"github.com/rpop-project/rpop/internal/accesslog"
	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/southbound"
	"github.com/rpop-project/rpop/internal/store"
)

func TestSouthboundLogsBasicUploadAcksAndWrites(t *testing.T) {
	h := newIngestHarness(t)
	token := h.createNode("edge-1")
	identity := h.register(token)
	h.placeSite("site-a", "edge-1", "default")
	client := nodeClient(identity)

	line := accessEnvelope(t, "default", accesslog.Record{SiteID: "site-a", Method: "GET", Path: "/x", Status: 200})
	response := h.uploadSegment(client, 1, []string{line})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d", response.StatusCode)
	}
	if ack := decodeAck(t, response); ack.Ack != 1 {
		t.Fatalf("ack = %#v, want 1", ack)
	}
}

// TestSouthboundLogsRejectsMalformedRequests checks the request-shape validations that do not depend on any
// segment content.
func TestSouthboundLogsRejectsMalformedRequests(t *testing.T) {
	h := newIngestHarness(t)
	token := h.createNode("edge-1")
	identity := h.register(token)
	client := nodeClient(identity)
	unauthenticated := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}

	tests := []struct {
		name       string
		client     *http.Client
		segment    string
		encoding   string
		body       []byte
		wantStatus int
	}{
		{"no client certificate", unauthenticated, "1", "gzip", nil, http.StatusUnauthorized},
		{"missing segment header", client, "", "gzip", nil, http.StatusBadRequest},
		{"non-numeric segment header", client, "abc", "gzip", nil, http.StatusBadRequest},
		{"missing content-encoding", client, "1", "", nil, http.StatusBadRequest},
		{"invalid gzip body", client, "1", "gzip", []byte("not gzip"), http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodPost, h.southbound.URL+southbound.LogsPath, strings.NewReader(string(tc.body)))
			if err != nil {
				t.Fatal(err)
			}
			if tc.segment != "" {
				request.Header.Set(southbound.LogSegmentHeader, tc.segment)
			}
			if tc.encoding != "" {
				request.Header.Set("Content-Encoding", tc.encoding)
			}
			response, err := tc.client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", response.StatusCode, tc.wantStatus)
			}
		})
	}
}

// TestSouthboundLogsRejectsOutOfRangeSegmentNumbers covers stage 5 security review item 1: a segment number the
// store could never persist as a high-water mark must be rejected before the controller does any of a segment's
// real work (decompressing or writing its records), not after. Each case uses its own node so an out-of-range
// attempt in one case can never be mistaken for an idempotent replay against another case's high-water mark.
// store.MaxLogHWM itself is seeded close to the node's current high-water mark first, so this test exercises
// only the range check (item 1), not item 2's separate bound on how far a single request may jump ahead.
func TestSouthboundLogsRejectsOutOfRangeSegmentNumbers(t *testing.T) {
	h := newIngestHarness(t)
	tests := []struct {
		name       string
		seedHWM    uint64
		segment    uint64
		wantStatus int
	}{
		{"zero is below the 1-based valid range", 0, 0, http.StatusBadRequest},
		{"one is the lowest valid segment", 0, 1, http.StatusOK},
		{"store.MaxLogHWM is the highest valid segment", store.MaxLogHWM - 1, store.MaxLogHWM, http.StatusOK},
		{"store.MaxLogHWM plus one has the high bit set", 0, uint64(math.MaxInt64) + 1, http.StatusBadRequest},
		{"MaxUint64 has the high bit set", 0, math.MaxUint64, http.StatusBadRequest},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nodeID := fmt.Sprintf("edge-bounds-%d", i)
			token := h.createNode(nodeID)
			identity := h.register(token)
			client := nodeClient(identity)
			if tc.seedHWM != 0 {
				if err := h.control.store.UpdateNodeLogHWM(context.Background(), nodeID, tc.seedHWM); err != nil {
					t.Fatal(err)
				}
			}

			response := h.uploadSegment(client, tc.segment, nil)
			defer response.Body.Close()
			if response.StatusCode != tc.wantStatus {
				t.Fatalf("segment %d: status = %d, want %d", tc.segment, response.StatusCode, tc.wantStatus)
			}
			node, err := h.control.store.GetNode(context.Background(), nodeID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantStatus == http.StatusOK {
				if node.LogHWM != tc.segment {
					t.Fatalf("LogHWM = %d, want %d", node.LogHWM, tc.segment)
				}
				return
			}
			if node.LogHWM != tc.seedHWM {
				t.Fatalf("LogHWM = %d, want %d (an out-of-range segment must not be written or acknowledged)", node.LogHWM, tc.seedHWM)
			}
		})
	}
}

// TestSouthboundLogsBoundsSegmentJumpsAndEscalatesLargeOnesToWarn covers stage 5 security review item 2: a
// node's log segment sequence is allowed to jump ahead of its high-water mark (a legitimate gap from a spool
// dropping segments under quota pressure, D25), but a single request must not be able to jump it arbitrarily far
// ahead, and a jump large enough to be unusual must be visible to an operator at Warn, not buried at Info.
func TestSouthboundLogsBoundsSegmentJumpsAndEscalatesLargeOnesToWarn(t *testing.T) {
	h := newIngestHarness(t)

	t.Run("a routine jump is accepted and logged at info", func(t *testing.T) {
		token := h.createNode("edge-routine-jump")
		identity := h.register(token)
		client := nodeClient(identity)
		h.logs.TakeAll()
		response := h.uploadSegment(client, 5, nil)
		if ack := decodeAck(t, response); ack.Ack != 5 {
			t.Fatalf("ack = %#v, want 5", ack)
		}
		foundInfo, foundWarn := false, false
		for _, entry := range h.logs.All() {
			if strings.Contains(entry.Message, "jumped ahead") {
				switch entry.Level {
				case zapcore.InfoLevel:
					foundInfo = true
				case zapcore.WarnLevel:
					foundWarn = true
				}
			}
		}
		if !foundInfo || foundWarn {
			t.Fatalf("routine jump: foundInfo=%v foundWarn=%v, want info only", foundInfo, foundWarn)
		}
	})

	t.Run("a jump past the warn threshold but within the hard bound is accepted and logged at warn", func(t *testing.T) {
		token := h.createNode("edge-large-jump")
		identity := h.register(token)
		client := nodeClient(identity)
		h.logs.TakeAll()
		response := h.uploadSegment(client, warnLogSegmentJumpThreshold+2, nil)
		if ack := decodeAck(t, response); ack.Ack != warnLogSegmentJumpThreshold+2 {
			t.Fatalf("ack = %#v, want %d", ack, warnLogSegmentJumpThreshold+2)
		}
		foundWarn := false
		for _, entry := range h.logs.All() {
			if strings.Contains(entry.Message, "jumped ahead") && entry.Level == zapcore.WarnLevel {
				foundWarn = true
			}
		}
		if !foundWarn {
			t.Fatal("expected a warn-level log entry for the large jump")
		}
	})

	t.Run("a jump past the hard bound is rejected and the high-water mark does not move", func(t *testing.T) {
		token := h.createNode("edge-excessive-jump")
		identity := h.register(token)
		client := nodeClient(identity)
		response := h.uploadSegment(client, maxLogSegmentJump+2, nil)
		defer response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", response.StatusCode)
		}
		node, err := h.control.store.GetNode(context.Background(), "edge-excessive-jump")
		if err != nil {
			t.Fatal(err)
		}
		if node.LogHWM != 0 {
			t.Fatalf("LogHWM = %d, want 0 (an excessive jump must not be written or acknowledged)", node.LogHWM)
		}
	})
}

// TestSouthboundLogsRejectsTheEmbeddedNodeIdentity is a defense-in-depth check: the
// embedded node has no row in the nodes table, so authenticateNode already rejects a certificate presented for
// LocalNodeID (unauthenticated, not merely "wrong kind of node"); only the controller itself can mint that
// certificate to begin with. Either way, the request must never be accepted.
func TestSouthboundLogsRejectsTheEmbeddedNodeIdentity(t *testing.T) {
	h := newIngestHarness(t)
	ca, err := h.control.ensureCA(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, csrPEM, err := pki.NewKeyAndCSR(LocalNodeID)
	if err != nil {
		t.Fatal(err)
	}
	_, certPEM, err := ca.SignNode(csrPEM, LocalNodeID, localGeneration)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := pki.LoadIdentity(certPEM, keyPEM, ca.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	response := h.uploadSegment(nodeClient(identity), 1, []string{})
	if response.StatusCode == http.StatusOK {
		t.Fatalf("status = %d, want a rejection", response.StatusCode)
	}
	_ = response.Body.Close()
}

// TestSouthboundLogsHWMIdempotencyAndSkipAhead covers D24's idempotent-replay and legitimate-gap behavior, plus
// the node identity override on tunnel events (a node cannot attribute an event to another node's hop).
func TestSouthboundLogsHWMIdempotencyAndSkipAhead(t *testing.T) {
	h := newIngestHarness(t)
	token := h.createNode("edge-1")
	identity := h.register(token)
	h.placeSite("site-a", "edge-1", "default")
	client := nodeClient(identity)

	accessLine := accessEnvelope(t, "default", accesslog.Record{SiteID: "site-a", Method: "GET", Path: "/one", Status: 200})
	tunnelLine := tunnelEnvelope(t, overlay.TunnelEvent{
		Timestamp: time.Now(), TunnelID: "tunnel-1", NodeID: "someone-else", Role: overlay.RoleEntry, Stage: overlay.StageArrived,
	})

	first := h.uploadSegment(client, 1, []string{accessLine, tunnelLine})
	if ack := decodeAck(t, first); ack.Ack != 1 {
		t.Fatalf("first upload ack = %#v, want 1", ack)
	}
	result, err := h.control.accessLogs.Search(context.Background(), "default", accesslog.Query{SiteID: "site-a", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 {
		t.Fatalf("access log total = %d, want 1", result.Total)
	}
	events, err := h.control.tunnelEvents.Query(context.Background(), "tunnel-1", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].NodeID != "edge-1" {
		t.Fatalf("tunnel events = %#v, want one event attributed to edge-1 (not the spoofed node id)", events)
	}

	// Re-sending the same segment must ack without writing again.
	replay := h.uploadSegment(client, 1, []string{accessLine, tunnelLine})
	if ack := decodeAck(t, replay); ack.Ack != 1 {
		t.Fatalf("replay ack = %#v, want 1", ack)
	}
	result, err = h.control.accessLogs.Search(context.Background(), "default", accesslog.Query{SiteID: "site-a", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 {
		t.Fatalf("access log total after replay = %d, want still 1 (no duplicate write)", result.Total)
	}

	// A legitimate gap (the node's spool dropped a segment under quota, D25): segment 5 after HWM 1 is accepted.
	h.logs.TakeAll()
	skipAhead := h.uploadSegment(client, 5, []string{accessLine})
	if ack := decodeAck(t, skipAhead); ack.Ack != 5 {
		t.Fatalf("skip-ahead ack = %#v, want 5", ack)
	}
	foundSkipLog := false
	for _, entry := range h.logs.All() {
		if strings.Contains(entry.Message, "jumped ahead") {
			foundSkipLog = true
		}
	}
	if !foundSkipLog {
		t.Fatal("expected a log entry noting the sequence gap")
	}

	// A segment at or below the current HWM (3, say) is still an idempotent replay even though it was never the
	// segment that last advanced the mark.
	old := h.uploadSegment(client, 3, []string{})
	if ack := decodeAck(t, old); ack.Ack != 5 {
		t.Fatalf("old segment ack = %#v, want 5 (the current HWM)", ack)
	}
}

// TestSouthboundLogsCountsBadLinesWithoutFailingTheSegment covers D24's "a corrupt or unplaceable line must not
// block the rest of the segment" requirement.
func TestSouthboundLogsCountsBadLinesWithoutFailingTheSegment(t *testing.T) {
	h := newIngestHarness(t)
	token := h.createNode("edge-1")
	identity := h.register(token)
	h.placeSite("site-a", "edge-1", "default")
	client := nodeClient(identity)

	goodLine := accessEnvelope(t, "default", accesslog.Record{SiteID: "site-a", Method: "GET", Path: "/ok", Status: 200})
	missingAdapter := accessEnvelope(t, "does-not-exist", accesslog.Record{SiteID: "site-a", Method: "GET", Path: "/bad-adapter", Status: 200})
	unrelatedAdapter := accessEnvelope(t, "unrelated", accesslog.Record{SiteID: "site-b", Method: "GET", Path: "/unrelated", Status: 200})
	h.createAdapter("Unrelated", accesslog.DefaultConfig())
	// "unrelated" now exists, but no site placed on edge-1 selects it (accesslog adapter id assigned by the
	// controller, not "unrelated" itself; re-derive the actual id from the config listing).
	var adapters loggingConfigResponse
	if err := json.Unmarshal(h.call(http.MethodGet, "/api/logging", "", http.StatusOK).Body.Bytes(), &adapters); err != nil {
		t.Fatal(err)
	}
	var unrelatedID string
	for _, adapter := range adapters.Adapters {
		if adapter.Name == "Unrelated" {
			unrelatedID = adapter.ID
		}
	}
	if unrelatedID == "" {
		t.Fatal("could not find the created adapter")
	}
	unrelatedAdapterLine := accessEnvelope(t, unrelatedID, accesslog.Record{SiteID: "site-b", Method: "GET", Path: "/unrelated", Status: 200})

	response := h.uploadSegment(client, 1, []string{
		goodLine, "not json at all", missingAdapter, unrelatedAdapter, unrelatedAdapterLine,
		`{"kind":"unknown-kind","record":{}}`, `{"kind":"access","adapterId":"default","record":"not-a-record-object"}`,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want 200 despite the bad lines", response.StatusCode)
	}
	if ack := decodeAck(t, response); ack.Ack != 1 {
		t.Fatalf("ack = %#v, want 1", ack)
	}
	result, err := h.control.accessLogs.Search(context.Background(), "default", accesslog.Query{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Records[0].Path != "/ok" {
		t.Fatalf("default adapter records = %#v, want exactly the good line", result)
	}
}

// TestSouthboundLogsWriteFailureDoesNotAdvanceHWM covers D24's "any write failure returns an error and the
// high-water mark does not advance" requirement, and that a subsequent, successfully-routed retry does advance
// it (modeling an operator fixing a site's adapter selection).
func TestSouthboundLogsWriteFailureDoesNotAdvanceHWM(t *testing.T) {
	h := newIngestHarness(t)
	token := h.createNode("edge-1")
	identity := h.register(token)
	client := nodeClient(identity)

	// A ClickHouse adapter pointed at a closed port: every write fails immediately with a connection error.
	broken := httptest.NewServer(nil)
	broken.Close()
	brokenID := h.createAdapter("Broken", accesslog.Config{Adapter: "clickhouse", ClickHouse: accesslog.ClickHouseConfig{URL: broken.URL, Database: "default", Table: "access_logs"}})
	h.placeSite("site-a", "edge-1", brokenID)

	failingLine := accessEnvelope(t, brokenID, accesslog.Record{SiteID: "site-a", Method: "GET", Path: "/x", Status: 200})
	response := h.uploadSegment(client, 1, []string{failingLine})
	if response.StatusCode == http.StatusOK {
		t.Fatal("expected the upload to fail while the adapter is unreachable")
	}
	_ = response.Body.Close()
	node, err := h.control.store.GetNode(context.Background(), "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	if node.LogHWM != 0 {
		t.Fatalf("LogHWM = %d after a failed write, want 0", node.LogHWM)
	}

	// The operator repoints the site at a working adapter; the node resends the same, still-unacked segment.
	h.call(http.MethodPut, "/api/sites/site-a", `{"id":"site-a","name":"site-a","config":{"nodes":["edge-1"],"listenPort":8080,"upstreams":[{"url":"http://upstream.test"}],"accessLog":{"adapterId":"default"}}}`, http.StatusOK)
	workingLine := accessEnvelope(t, "default", accesslog.Record{SiteID: "site-a", Method: "GET", Path: "/x", Status: 200})
	retry := h.uploadSegment(client, 1, []string{workingLine})
	if ack := decodeAck(t, retry); ack.Ack != 1 {
		t.Fatalf("retry ack = %#v, want 1", ack)
	}
	node, err = h.control.store.GetNode(context.Background(), "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	if node.LogHWM != 1 {
		t.Fatalf("LogHWM after the successful retry = %d, want 1", node.LogHWM)
	}
}

// TestSouthboundLogsHWMSurvivesControllerRestart covers D24's requirement that a controller restart never
// replays or loses records: the durable high-water mark comes from SQLite, not the in-memory node registry.
func TestSouthboundLogsHWMSurvivesControllerRestart(t *testing.T) {
	h := newIngestHarness(t)
	token := h.createNode("edge-1")
	identity := h.register(token)
	h.placeSite("site-a", "edge-1", "default")
	line := accessEnvelope(t, "default", accesslog.Record{SiteID: "site-a", Method: "GET", Path: "/x", Status: 200})
	if ack := decodeAck(t, h.uploadSegment(nodeClient(identity), 1, []string{line})); ack.Ack != 1 {
		t.Fatalf("ack = %#v, want 1", ack)
	}

	h.restart()

	// The node's TLS identity was issued by the same internal CA, persisted in and reloaded from the store, so
	// it still authenticates after the restart.
	replay := h.uploadSegment(nodeClient(identity), 1, []string{line})
	if ack := decodeAck(t, replay); ack.Ack != 1 {
		t.Fatalf("post-restart replay ack = %#v, want 1 (idempotent, not re-written)", ack)
	}
	result, err := h.control.accessLogs.Search(context.Background(), "default", accesslog.Query{SiteID: "site-a", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 {
		t.Fatalf("access log total after restart = %d, want still 1", result.Total)
	}
}

// TestSouthboundLogsSerializesConcurrentUploadsFromTheSameNode covers D24's per-node serialization requirement:
// many requests racing to upload the same segment must produce exactly one write and a consistent final HWM,
// never a torn read-modify-write of the high-water mark.
func TestSouthboundLogsSerializesConcurrentUploadsFromTheSameNode(t *testing.T) {
	h := newIngestHarness(t)
	token := h.createNode("edge-1")
	identity := h.register(token)
	h.placeSite("site-a", "edge-1", "default")
	client := nodeClient(identity)
	line := accessEnvelope(t, "default", accesslog.Record{SiteID: "site-a", Method: "GET", Path: "/x", Status: 200})

	const attempts = 8
	var wg sync.WaitGroup
	acks := make([]southbound.LogAck, attempts)
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			acks[i] = decodeAck(t, h.uploadSegment(client, 1, []string{line}))
		}(i)
	}
	wg.Wait()
	for i, ack := range acks {
		if ack.Ack != 1 {
			t.Fatalf("attempt %d ack = %#v, want 1", i, ack)
		}
	}
	node, err := h.control.store.GetNode(context.Background(), "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	if node.LogHWM != 1 {
		t.Fatalf("LogHWM = %d, want 1", node.LogHWM)
	}
	result, err := h.control.accessLogs.Search(context.Background(), "default", accesslog.Query{SiteID: "site-a", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 {
		t.Fatalf("access log total = %d, want exactly 1 (the lock must serialize the racing uploads)", result.Total)
	}
}

func TestSouthboundLogsRejectsOversizedSegmentBody(t *testing.T) {
	h := newIngestHarness(t)
	token := h.createNode("edge-1")
	identity := h.register(token)
	client := nodeClient(identity)

	request, err := http.NewRequest(http.MethodPost, h.southbound.URL+southbound.LogsPath, randomGzipStream(southbound.MaxLogSegmentBytes+(1<<20)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Encoding", "gzip")
	request.Header.Set(southbound.LogSegmentHeader, "1")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK {
		t.Fatal("expected the oversized segment to be rejected")
	}
	node, err := h.control.store.GetNode(context.Background(), "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	if node.LogHWM != 0 {
		t.Fatalf("LogHWM = %d, want 0 (the oversized segment must not advance it)", node.LogHWM)
	}
}

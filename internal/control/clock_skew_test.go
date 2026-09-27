package control

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/southbound"
	"github.com/rpop-project/rpop/internal/store"
	"github.com/rpop-project/rpop/internal/traceid"
)

// findNodeView fetches GET /api/nodes and returns id's entry, so an HTTP-level test can assert on the fields the
// console actually receives rather than reaching back into the controller's internals.
func findNodeView(t *testing.T, h *ingestHarness, id string) nodeView {
	t.Helper()
	var nodes []nodeView
	if err := json.Unmarshal(h.call(http.MethodGet, "/api/nodes", "", http.StatusOK).Body.Bytes(), &nodes); err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if node.ID == id {
			return node
		}
	}
	t.Fatalf("node %q is not listed", id)
	return nodeView{}
}

// TestNodeViewExposesClockSkewAndWarnStatus covers D28's console-facing half: nodeView surfaces whatever offset
// the node last reported, marked "warn" once it exceeds the configured threshold and "ok" otherwise, and both
// fields stay nil/empty for a node that never reported one — the same empty-until-first-report convention
// ProtocolVersion/ProtocolStatus already use.
func TestNodeViewExposesClockSkewAndWarnStatus(t *testing.T) {
	c := newTestControl(t)
	small, large, negLarge := int64(500), int64(2500), int64(-3000)
	c.nodes.report("edge-1", southbound.Status{ClockOffsetMillis: &small})
	c.nodes.report("edge-2", southbound.Status{ClockOffsetMillis: &large})
	c.nodes.report("edge-3", southbound.Status{ClockOffsetMillis: &negLarge})

	tests := []struct {
		id         string
		wantMillis int64
		wantStatus string
	}{
		{"edge-1", small, "ok"},
		{"edge-2", large, "warn"},
		{"edge-3", negLarge, "warn"},
	}
	for _, tt := range tests {
		view := c.nodeView(store.Node{ID: tt.id})
		if view.ClockSkewMillis == nil || *view.ClockSkewMillis != tt.wantMillis {
			t.Fatalf("%s ClockSkewMillis = %v, want %d", tt.id, view.ClockSkewMillis, tt.wantMillis)
		}
		if view.ClockSkewStatus != tt.wantStatus {
			t.Fatalf("%s ClockSkewStatus = %q, want %q", tt.id, view.ClockSkewStatus, tt.wantStatus)
		}
	}

	// A node that never reported a status at all carries neither field.
	unreported := c.nodeView(store.Node{ID: "edge-4"})
	if unreported.ClockSkewMillis != nil || unreported.ClockSkewStatus != "" {
		t.Fatalf("unreported node clock skew = %#v, want nil/empty", unreported)
	}
}

// TestSetClockSkewWarnThresholdOverridesTheDefault covers the D28 "可配" knob: raising the threshold turns a
// report that used to warn into one that no longer does.
func TestSetClockSkewWarnThresholdOverridesTheDefault(t *testing.T) {
	c := newTestControl(t)
	offset := int64(2500)
	c.nodes.report("edge-1", southbound.Status{ClockOffsetMillis: &offset})
	if view := c.nodeView(store.Node{ID: "edge-1"}); view.ClockSkewStatus != "warn" {
		t.Fatalf("ClockSkewStatus = %q before raising the threshold, want warn", view.ClockSkewStatus)
	}
	c.SetClockSkewWarnThreshold(3000)
	if view := c.nodeView(store.Node{ID: "edge-1"}); view.ClockSkewStatus != "ok" {
		t.Fatalf("ClockSkewStatus = %q after raising the threshold above the reported offset, want ok", view.ClockSkewStatus)
	}
	// ms <= 0 is a no-op, not a reset to some other default.
	c.SetClockSkewWarnThreshold(0)
	if view := c.nodeView(store.Node{ID: "edge-1"}); view.ClockSkewStatus != "ok" {
		t.Fatalf("ClockSkewStatus = %q after a no-op SetClockSkewWarnThreshold(0), want it to stay ok", view.ClockSkewStatus)
	}
}

// TestTopologyExposesClockSkewForEveryNodeKind covers GET /api/topology's own copy of the same fields: a
// reporting registered node, an embedded node (always exactly 0), and a registered node that never reported.
func TestTopologyExposesClockSkewForEveryNodeKind(t *testing.T) {
	c := newTestControl(t)
	c.SetEmbeddedNode(true)
	offset := int64(2500)
	c.nodes.report("edge-1", southbound.Status{ClockOffsetMillis: &offset})
	if err := c.store.SaveNode(t.Context(), store.Node{ID: "edge-1"}); err != nil {
		t.Fatal(err)
	}
	if err := c.store.SaveNode(t.Context(), store.Node{ID: "edge-2"}); err != nil {
		t.Fatal(err)
	}

	view, err := c.buildTopology(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reporting := findTopologyNode(t, view.Nodes, "edge-1")
	if reporting.ClockSkewMillis == nil || *reporting.ClockSkewMillis != offset || reporting.ClockSkewStatus != "warn" {
		t.Fatalf("edge-1 topology node = %#v", reporting)
	}
	unreported := findTopologyNode(t, view.Nodes, "edge-2")
	if unreported.ClockSkewMillis != nil || unreported.ClockSkewStatus != "" {
		t.Fatalf("edge-2 (never reported) topology node = %#v, want nil/empty", unreported)
	}
	embedded := findTopologyNode(t, view.Nodes, LocalNodeID)
	if embedded.ClockSkewMillis == nil || *embedded.ClockSkewMillis != 0 || embedded.ClockSkewStatus != "ok" {
		t.Fatalf("embedded node topology entry = %#v, want clock skew exactly 0/ok", embedded)
	}
}

// TestLocalNodeViewClockSkewIsAlwaysZero covers nodeView's own embedded-node path (localNodeView): it runs in
// the same process and clock as the controller, so there is nothing to measure.
func TestLocalNodeViewClockSkewIsAlwaysZero(t *testing.T) {
	c := newTestControl(t)
	view := c.localNodeView()
	if view.ClockSkewMillis == nil || *view.ClockSkewMillis != 0 || view.ClockSkewStatus != "ok" {
		t.Fatalf("localNodeView clock skew = %#v, want exactly 0/ok", view)
	}
}

// TestTunnelEventViewsAttachTheReportingNodesCurrentClockSkew covers D28's tunnel-timeline annotation: each
// event's ClockSkewMillis is the reporting node's *current* known skew (not a historical reconstruction), nil for
// a node that never reported one, and always exactly 0 for the embedded node.
func TestTunnelEventViewsAttachTheReportingNodesCurrentClockSkew(t *testing.T) {
	c := newTestControl(t)
	offset := int64(1200)
	c.nodes.report("entry-1", southbound.Status{ClockOffsetMillis: &offset})
	events := []overlay.TunnelEvent{
		{TunnelID: "t1", NodeID: "entry-1", Role: overlay.RoleEntry, Stage: overlay.StageArrived},
		{TunnelID: "t1", NodeID: "exit-1", Role: overlay.RoleExit, Stage: overlay.StageEnded},
		{TunnelID: "t1", NodeID: LocalNodeID, Role: overlay.RoleRelay, Stage: overlay.StageEstablished},
	}
	views := c.tunnelEventViews(events)
	if len(views) != 3 {
		t.Fatalf("views = %d, want 3", len(views))
	}
	if views[0].ClockSkewMillis == nil || *views[0].ClockSkewMillis != offset {
		t.Fatalf("entry-1 view = %#v, want ClockSkewMillis %d", views[0], offset)
	}
	if views[1].ClockSkewMillis != nil {
		t.Fatalf("exit-1 (never reported) view = %#v, want nil", views[1])
	}
	if views[2].ClockSkewMillis == nil || *views[2].ClockSkewMillis != 0 {
		t.Fatalf("embedded node view = %#v, want exactly 0", views[2])
	}
	// The underlying event fields must still round-trip unchanged.
	if views[0].TunnelID != "t1" || views[0].Role != overlay.RoleEntry {
		t.Fatalf("event fields not preserved: %#v", views[0])
	}
}

// TestSouthboundStatusReportsClockSkewOverHTTP is the end-to-end counterpart of the unit tests above: a real
// status POST carrying SentAt/ClockOffsetMillis/ClockRTTMillis gets a real 200 + southbound.StatusResponse
// answer, and GET /api/nodes/{id} then exposes the matching clockSkewMillis/clockSkewStatus.
func TestSouthboundStatusReportsClockSkewOverHTTP(t *testing.T) {
	h := newIngestHarness(t)
	identity := h.register(h.createNode("skew-node"))
	client := nodeClient(identity)
	defer client.CloseIdleConnections()

	offset, rtt := int64(2500), int64(40)
	status := southbound.Status{Version: "test", Running: []string{}, SentAt: time.Now(), ClockOffsetMillis: &offset, ClockRTTMillis: &rtt}
	body, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Post(h.southbound.URL+southbound.StatusPath, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status upload = %d, want 200 (D28: the controller now always answers with a body)", response.StatusCode)
	}
	var decoded southbound.StatusResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode StatusResponse: %v", err)
	}
	if decoded.ReceivedAt.IsZero() || decoded.RespondedAt.IsZero() {
		t.Fatalf("StatusResponse = %#v, want both timestamps set", decoded)
	}

	view := findNodeView(t, h, "skew-node")
	if view.ClockSkewMillis == nil || *view.ClockSkewMillis != offset || view.ClockSkewStatus != "warn" {
		t.Fatalf("node view clock skew = %#v", view)
	}
}

// TestSouthboundStatusFromANodePredatingD28StillGetsAJSONResponse covers the "old node x new controller" half of
// D28's compatibility (the "new node x old controller" half lives in internal/agent, where postJSON's 204
// handling is exercised): a status with no SentAt/ClockOffsetMillis (as any pre-D28 node sends) still gets 200
// with a full StatusResponse body, and the node's view carries no clock skew since it never reported one.
func TestSouthboundStatusFromANodePredatingD28StillGetsAJSONResponse(t *testing.T) {
	h := newIngestHarness(t)
	identity := h.register(h.createNode("old-node"))
	client := nodeClient(identity)
	defer client.CloseIdleConnections()

	body, err := json.Marshal(southbound.Status{Version: "old", Running: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Post(h.southbound.URL+southbound.StatusPath, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status upload = %d, want 200", response.StatusCode)
	}
	var decoded southbound.StatusResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode StatusResponse: %v", err)
	}
	if decoded.ReceivedAt.IsZero() || decoded.RespondedAt.IsZero() {
		t.Fatalf("StatusResponse = %#v, want both timestamps set even for a node that sent none of its own", decoded)
	}

	view := findNodeView(t, h, "old-node")
	if view.ClockSkewMillis != nil || view.ClockSkewStatus != "" {
		t.Fatalf("node view clock skew = %#v, want nil/empty for a node that never reported one", view)
	}
}

// TestLoggingTunnelEventsAttachesClockSkewOverHTTP covers GET /api/logging/tunnels/{tunnelId} end to end: once a
// node's clock skew is known, the query response's events carry it, keyed by each event's own reporting node.
func TestLoggingTunnelEventsAttachesClockSkewOverHTTP(t *testing.T) {
	h := newIngestHarness(t)
	entryToken := h.createNode("edge-1")
	entryIdentity := h.register(entryToken)
	h.placeSite("site-a", "edge-1", "default")

	offset := int64(1800)
	h.control.nodes.report("edge-1", southbound.Status{ClockOffsetMillis: &offset})

	tunnelID := traceid.New()
	event := tunnelEnvelope(t, overlay.TunnelEvent{Timestamp: time.Now(), TunnelID: tunnelID, NodeID: "edge-1", Role: overlay.RoleEntry, Stage: overlay.StageArrived})
	if ack := decodeAck(t, h.uploadSegment(nodeClient(entryIdentity), 1, []string{event})); ack.Ack != 1 {
		t.Fatalf("tunnel event upload ack = %#v", ack)
	}

	response := h.call(http.MethodGet, "/api/logging/tunnels/"+tunnelID, "", http.StatusOK)
	var views []struct {
		NodeID          string `json:"nodeId"`
		ClockSkewMillis *int64 `json:"clockSkewMillis"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].NodeID != "edge-1" {
		t.Fatalf("tunnel events = %#v, want exactly one from edge-1", views)
	}
	if views[0].ClockSkewMillis == nil || *views[0].ClockSkewMillis != offset {
		t.Fatalf("event clockSkewMillis = %v, want %d", views[0].ClockSkewMillis, offset)
	}
}

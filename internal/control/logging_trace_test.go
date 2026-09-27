package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rpop-project/rpop/internal/accesslog"
	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/traceid"
)

func TestLoggingTraceAndTunnelEventsAPIs(t *testing.T) {
	h := newIngestHarness(t)
	entryToken, exitToken := h.createNode("edge-1"), h.createNode("edge-2")
	entryIdentity, exitIdentity := h.register(entryToken), h.register(exitToken)
	h.placeSite("site-a", "edge-1", "default")

	trackID, tunnelID := traceid.New(), traceid.New()
	accessLine := accessEnvelope(t, "default", accesslog.Record{
		SiteID: "site-a", TrackID: trackID, TunnelID: tunnelID, Method: "GET", Path: "/x", Status: 200,
	})
	if ack := decodeAck(t, h.uploadSegment(nodeClient(entryIdentity), 1, []string{accessLine})); ack.Ack != 1 {
		t.Fatalf("access upload ack = %#v", ack)
	}

	base := time.Now().UTC()
	entryEvent := tunnelEnvelope(t, overlay.TunnelEvent{Timestamp: base, TunnelID: tunnelID, Role: overlay.RoleEntry, Stage: overlay.StageArrived})
	exitEvent := tunnelEnvelope(t, overlay.TunnelEvent{Timestamp: base.Add(time.Second), TunnelID: tunnelID, Role: overlay.RoleExit, Stage: overlay.StageEnded})
	if ack := decodeAck(t, h.uploadSegment(nodeClient(entryIdentity), 2, []string{entryEvent})); ack.Ack != 2 {
		t.Fatalf("entry tunnel event upload ack = %#v", ack)
	}
	if ack := decodeAck(t, h.uploadSegment(nodeClient(exitIdentity), 1, []string{exitEvent})); ack.Ack != 1 {
		t.Fatalf("exit tunnel event upload ack = %#v", ack)
	}

	traceResponse := h.call(http.MethodGet, "/api/logging/trace/"+trackID, "", http.StatusOK)
	var record accesslog.Record
	if err := json.Unmarshal(traceResponse.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.TunnelID != tunnelID || record.Path != "/x" {
		t.Fatalf("trace record = %#v", record)
	}

	h.call(http.MethodGet, "/api/logging/trace/not-a-uuid", "", http.StatusBadRequest)
	h.call(http.MethodGet, "/api/logging/trace/"+traceid.New(), "", http.StatusNotFound)

	tunnelsResponse := h.call(http.MethodGet, "/api/logging/tunnels/"+tunnelID, "", http.StatusOK)
	var events []overlay.TunnelEvent
	if err := json.Unmarshal(tunnelsResponse.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].NodeID != "edge-1" || events[0].Role != overlay.RoleEntry || events[1].NodeID != "edge-2" || events[1].Role != overlay.RoleExit {
		t.Fatalf("tunnel events = %#v, want entry (edge-1) then exit (edge-2)", events)
	}

	h.call(http.MethodGet, "/api/logging/tunnels/not-a-uuid", "", http.StatusBadRequest)
	emptyResponse := h.call(http.MethodGet, "/api/logging/tunnels/"+traceid.New(), "", http.StatusOK)
	var empty []overlay.TunnelEvent
	if err := json.Unmarshal(emptyResponse.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("events for an unknown tunnel id = %#v, want empty", empty)
	}
}

// TestLoggingTraceAndTunnelEventsRejectWrongMethodAndMissingStores covers the two query endpoints' remaining
// error paths: a non-GET method, and a controller built without NewWithLogDir (no log stores at all).
func TestLoggingTraceAndTunnelEventsRejectWrongMethodAndMissingStores(t *testing.T) {
	handler := newTestControl(t).Handler()
	cookie := setupAdminForTest(t, handler)
	call := func(method, path string, want int) {
		t.Helper()
		request := httptest.NewRequest(method, path, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, response.Code, want, response.Body.String())
		}
	}
	id := traceid.New()
	call(http.MethodPost, "/api/logging/trace/"+id, http.StatusMethodNotAllowed)
	call(http.MethodPost, "/api/logging/tunnels/"+id, http.StatusMethodNotAllowed)
	call(http.MethodGet, "/api/logging/trace/"+id, http.StatusServiceUnavailable)
	call(http.MethodGet, "/api/logging/tunnels/"+id, http.StatusServiceUnavailable)
}

// TestLoggingTraceSurfacesASearchFailureInsteadOfMaskingItAsNotFound covers the case where an adapter cannot be
// searched at all (as opposed to simply having no match): the trace lookup must not report "not found" as if it
// had checked everywhere.
func TestLoggingTraceSurfacesASearchFailureInsteadOfMaskingItAsNotFound(t *testing.T) {
	h := newIngestHarness(t)
	broken := httptest.NewServer(nil)
	broken.Close()
	h.createAdapter("Broken", accesslog.Config{Adapter: "clickhouse", ClickHouse: accesslog.ClickHouseConfig{URL: broken.URL, Database: "default", Table: "access_logs"}})
	h.call(http.MethodGet, "/api/logging/trace/"+traceid.New(), "", http.StatusBadGateway)
}

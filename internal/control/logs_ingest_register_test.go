package control

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/rpop-project/rpop/internal/accesslog"
	"github.com/rpop-project/rpop/internal/pki"
)

// TestSouthboundRegisterResetsLogHWM covers the requirement that re-registering a node (issuing it a fresh
// certificate generation) resets its log high-water mark to 0: the node's own spool restarts numbering from
// segment 1 once it loses its identity (reinstall, lost data directory, ...), and a stale, higher mark left over
// from the retired generation would make that first segment look like an already-acked replay and silently drop
// it (see southboundRegister's doc comment).
func TestSouthboundRegisterResetsLogHWM(t *testing.T) {
	h := newIngestHarness(t)
	token := h.createNode("edge-1")
	oldIdentity := h.register(token)
	h.placeSite("site-a", "edge-1", "default")
	line := accessEnvelope(t, "default", accesslog.Record{SiteID: "site-a", Method: "GET", Path: "/x", Status: 200})

	if ack := decodeAck(t, h.uploadSegment(nodeClient(oldIdentity), 1, []string{line})); ack.Ack != 1 {
		t.Fatalf("initial upload ack = %#v, want 1", ack)
	}
	node, err := h.control.store.GetNode(context.Background(), "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	if node.LogHWM != 1 {
		t.Fatalf("LogHWM before re-registration = %d, want 1", node.LogHWM)
	}

	newToken := h.reissueToken("edge-1")
	newIdentity := h.register(newToken)
	node, err = h.control.store.GetNode(context.Background(), "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	if node.LogHWM != 0 {
		t.Fatalf("LogHWM after re-registration = %d, want 0", node.LogHWM)
	}
	if node.CertGeneration != 2 {
		t.Fatalf("CertGeneration after re-registration = %d, want 2", node.CertGeneration)
	}

	// The new spool starts back at segment 1; it must be ingested, not treated as a replay of the old
	// generation's segment 1 (which already advanced the mark before the reset).
	response := h.uploadSegment(nodeClient(newIdentity), 1, []string{line})
	if ack := decodeAck(t, response); ack.Ack != 1 {
		t.Fatalf("post-registration segment 1 ack = %#v, want 1", ack)
	}
	result, err := h.control.accessLogs.Search(context.Background(), "default", accesslog.Query{SiteID: "site-a", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 {
		t.Fatalf("access log total after the post-registration segment = %d, want 2 (the old and the new generation's segment 1 both written)", result.Total)
	}

	// The old certificate's generation is retired; it can no longer authenticate at all, let alone upload.
	rejected := h.uploadSegment(nodeClient(oldIdentity), 2, []string{line})
	defer rejected.Body.Close()
	if rejected.StatusCode != http.StatusUnauthorized {
		t.Fatalf("upload with the retired generation's certificate = %d, want %d", rejected.StatusCode, http.StatusUnauthorized)
	}
}

// TestSouthboundRenewDoesNotResetLogHWM covers the other half of the same requirement: renewing a certificate
// keeps the same generation (see southboundRenew), so the node's spool has no reason to restart numbering, and
// the mark must not move.
func TestSouthboundRenewDoesNotResetLogHWM(t *testing.T) {
	h := newIngestHarness(t)
	token := h.createNode("edge-1")
	identity := h.register(token)
	h.placeSite("site-a", "edge-1", "default")
	line := accessEnvelope(t, "default", accesslog.Record{SiteID: "site-a", Method: "GET", Path: "/x", Status: 200})

	if ack := decodeAck(t, h.uploadSegment(nodeClient(identity), 1, []string{line})); ack.Ack != 1 {
		t.Fatalf("upload ack = %#v, want 1", ack)
	}

	h.renew(identity)

	node, err := h.control.store.GetNode(context.Background(), "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	if node.LogHWM != 1 {
		t.Fatalf("LogHWM after renewal = %d, want 1 (renewal must not reset it)", node.LogHWM)
	}
	if node.CertGeneration != 1 {
		t.Fatalf("CertGeneration after renewal = %d, want 1 (unchanged)", node.CertGeneration)
	}

	// The renewed certificate authenticates uploads exactly as the pre-renewal one did.
	response := h.uploadSegment(nodeClient(identity), 2, []string{line})
	if ack := decodeAck(t, response); ack.Ack != 2 {
		t.Fatalf("upload with the renewed certificate ack = %#v, want 2", ack)
	}
}

// TestSouthboundRegisterResetIsSerializedWithConcurrentUploads exercises the locking half of the requirement
// (southboundRegister's doc comment): the reset takes the same per-node lock southboundLogs does, so this is
// primarily a go test -race regression test — a missing lock would show up there as a data race on the node's
// high-water mark between the two handlers, not necessarily as a wrong final value.
func TestSouthboundRegisterResetIsSerializedWithConcurrentUploads(t *testing.T) {
	h := newIngestHarness(t)
	token := h.createNode("edge-1")
	oldIdentity := h.register(token)
	h.placeSite("site-a", "edge-1", "default")
	oldClient := nodeClient(oldIdentity)
	line := accessEnvelope(t, "default", accesslog.Record{SiteID: "site-a", Method: "GET", Path: "/x", Status: 200})

	const uploadAttempts = 50
	var wg sync.WaitGroup
	var newIdentity *pki.Identity
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := uint64(1); i <= uploadAttempts; i++ {
			// A concurrent re-registration may retire this identity mid-loop, and later attempts then get 401;
			// that is expected and not checked here, since the race detector's verdict on this test is the point.
			response := h.uploadSegment(oldClient, i, []string{line})
			_ = response.Body.Close()
		}
	}()
	go func() {
		defer wg.Done()
		newToken := h.reissueToken("edge-1")
		newIdentity = h.register(newToken)
	}()
	wg.Wait()

	node, err := h.control.store.GetNode(context.Background(), "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	if node.LogHWM != 0 {
		t.Fatalf("LogHWM after a re-registration racing concurrent uploads = %d, want 0", node.LogHWM)
	}

	// The new generation's spool restarting at segment 1 must still be accepted, not treated as a replay.
	response := h.uploadSegment(nodeClient(newIdentity), 1, []string{line})
	if ack := decodeAck(t, response); ack.Ack != 1 {
		t.Fatalf("post-registration segment 1 ack = %#v, want 1", ack)
	}
}

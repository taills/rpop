package control

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rpop-project/rpop/internal/southbound"
)

// TestSouthboundWatchSurvivesShortIdleAndHeaderTimeouts covers the remaining half of stage 5 security review
// item 5's southbound HTTP/2 server tuning, mirroring internal/overlay's relay port (see SouthboundHTTP2Config):
// ReadHeaderTimeout, IdleTimeout and MaxConcurrentStreams must never cut the long-lived watch stream short,
// since it is a single, ongoing response with nothing to wait for even while it carries nothing but an
// occasional ping (southbound.PingInterval). The timeouts here are injected far shorter than production
// (cmd/rpop's startSouthbound uses minutes) purely so the test does not have to wait that long to prove it.
func TestSouthboundWatchSurvivesShortIdleAndHeaderTimeouts(t *testing.T) {
	const shortTimeout = 50 * time.Millisecond
	h := newIngestHarnessWithSouthboundConfig(t, func(server *http.Server) {
		server.ReadHeaderTimeout = shortTimeout
		server.IdleTimeout = shortTimeout
		server.HTTP2 = SouthboundHTTP2Config()
	})
	token := h.createNode("watch-node")
	identity := h.register(token)

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   identity.ControllerClientConfig(),
		ForceAttemptHTTP2: true, // mirror internal/agent's real watch client (see its newClient).
	}}
	defer client.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, h.southbound.URL+southbound.WatchPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("watch: %d", response.StatusCode)
	}
	if response.ProtoMajor != 2 {
		t.Fatalf("watch negotiated %s, want HTTP/2 (mirroring production, see internal/agent's ForceAttemptHTTP2): "+
			"MaxConcurrentStreams only applies to it", response.Proto)
	}

	decoder := json.NewDecoder(response.Body)
	var first southbound.Frame
	if err := decoder.Decode(&first); err != nil {
		t.Fatalf("decode initial frame: %v", err)
	}
	if first.Type != southbound.FrameSnapshot {
		t.Fatalf("initial frame type = %q, want %q", first.Type, southbound.FrameSnapshot)
	}

	// Outlast several multiples of both injected timeouts before anything else happens on the connection.
	// Neither may have fired against watch's single, still-open response: renaming the node below must still
	// reach it afterwards.
	time.Sleep(10 * shortTimeout)
	h.call(http.MethodPut, "/api/nodes/watch-node", `{"id":"watch-node","name":"renamed"}`, http.StatusOK)

	var second southbound.Frame
	if err := decoder.Decode(&second); err != nil {
		t.Fatalf("decode frame after outlasting %v of idle/header timeout: %v (the stream was cut short)", 10*shortTimeout, err)
	}
	if second.Type != southbound.FrameSnapshot {
		t.Fatalf("frame after the rename type = %q, want %q", second.Type, southbound.FrameSnapshot)
	}
}

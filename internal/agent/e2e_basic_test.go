package agent

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rpop-project/rpop/internal/accesslog"
	"github.com/rpop-project/rpop/internal/dataplane"
)

// TestLogPipelineEndToEndBasicUploadAndQueries covers stage 5's primary path, node to controller, entirely
// through real components: a real node registers with a real controller over mTLS, serves a site with access
// logging on, forwards a request whose track id the ingress must mint itself (never trust a client-supplied
// one), spools the resulting record to a real temp-dir spool, uploads it over the real southbound listener, and
// the controller answers both the trace query and the node-health query built on top of it.
func TestLogPipelineEndToEndBasicUploadAndQueries(t *testing.T) {
	c := startLoggingController(t, nil)
	token := c.createNode("edge-1")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Track-Id", r.Header.Get(dataplane.TrackIDHeader))
		io.WriteString(w, "from origin")
	}))
	defer origin.Close()
	node, _ := startAgent(t, Config{ControllerURL: c.southbound.URL, JoinToken: token, DataDir: t.TempDir(), Version: "test"})
	eventually(t, "the node registers", func() bool { return c.node("edge-1").Online })

	port := freePort(t)
	c.createSite("web", "edge-1", "default", origin.URL, port)
	eventually(t, "the node serves the site", func() bool { return node.Engine().Running("web") })

	target := fmt.Sprintf("http://127.0.0.1:%d/", port)
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	const forged = "client-forged-track-id"
	request.Header.Set(dataplane.TrackIDHeader, forged)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "from origin" {
		t.Fatalf("body = %q", body)
	}
	trackID := response.Header.Get("X-Seen-Track-Id")
	if trackID == "" || trackID == forged {
		t.Fatalf("the ingress must overwrite a client-forged track id; origin saw %q", trackID)
	}

	waitForAck(t, node, 1)
	// statusLoop's periodic report (agent.go's statusInterval, 15s) is the only thing that pushes LogStats to the
	// controller; nothing about a log upload kicks an early one the way applying a new snapshot does.
	eventuallyWithin(t, 20*time.Second, "the node's status reaches the controller with the acked segment", func() bool {
		stats := c.nodeLogs("edge-1")
		return stats != nil && stats.AckedSegment >= 1
	})

	var record accesslog.Record
	eventuallyWithin(t, 10*time.Second, "the trace query finds the record the node uploaded", func() bool {
		found, code := c.trace(trackID)
		if code != http.StatusOK {
			return false
		}
		record = found
		return true
	})
	if record.TrackID != trackID {
		t.Fatalf("record.TrackID = %q, want %q", record.TrackID, trackID)
	}
	if record.SiteID != "web" {
		t.Fatalf("record.SiteID = %q, want %q (the site the node placed this request on)", record.SiteID, "web")
	}
	if record.Path != "/" || record.Status != http.StatusOK {
		t.Fatalf("record = %#v", record)
	}
}

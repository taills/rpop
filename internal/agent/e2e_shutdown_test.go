package agent

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLogPipelineCapturesFinalTunnelEventOnGracefulShutdown is a regression test for a defect this file's other
// end-to-end tests do not exercise: stopping a site through the console API (as
// TestLogPipelineTunnelTimelineAcrossEntryRelayAndExit does) closes its pooled connections and reports every
// hop's "ended" tunnel event just fine, but stopping the whole node process used to lose the entry hop's own
// "ended" event outright. agent.Run() closed the log spool (stopping its write goroutine) before stopping the
// engine and closing the overlay, so the "ended" event those two produce while shutting down was hopelessly
// enqueued into a spool no longer being drained (see Spool.Close's doc comment: writes after Close are
// structurally accepted but never persisted). This checks the record actually reaches the node's local spool
// directory - the property agent.Run()'s shutdown order is responsible for - rather than the controller, since
// the upload of that final segment is understood to happen on the node's next start, not before this process
// exits (its uploader already stopped by the time Run's deferred cleanup runs).
func TestLogPipelineCapturesFinalTunnelEventOnGracefulShutdown(t *testing.T) {
	c := startLoggingController(t, nil)
	entryToken := c.createNode("edge-1")
	exitToken := c.createRelayNode("edge-2", freeAddress(t))
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "from origin") }))
	defer origin.Close()

	dataDir := t.TempDir()
	entry, stopEntry := startAgent(t, Config{ControllerURL: c.southbound.URL, JoinToken: entryToken, DataDir: dataDir, Version: "test"})
	startAgent(t, Config{ControllerURL: c.southbound.URL, JoinToken: exitToken, DataDir: t.TempDir(), Version: "test"})
	eventually(t, "every node registers", func() bool { return c.inSync("edge-1", "edge-2") })

	port := freePort(t)
	site := fmt.Sprintf(`{"id":"web","name":"web","config":{"nodes":["edge-1"],"listenAddress":"127.0.0.1","listenPort":%d,
		"upstreams":[{"url":%q,"paths":[{"via":[{"node":"edge-2"}]}]}],"accessLog":{"adapterId":"default"}}}`, port, origin.URL)
	c.call(http.MethodPost, "/api/sites", site, http.StatusCreated)
	c.call(http.MethodPost, "/api/sites/web/start", "", http.StatusOK)
	eventually(t, "the path is up", func() bool { return entry.Engine().Running("web") && linksUp(entry) })

	target := fmt.Sprintf("http://127.0.0.1:%d/", port)
	if body := get(t, target); body != "from origin" {
		t.Fatalf("body = %q", body)
	}
	// Get the access log record (segment 1) safely uploaded and acked before stopping the node, so the pending
	// tunnel "ended" event this test cares about is unambiguously the only thing left in the spool at shutdown.
	waitForAck(t, entry, 1)

	stopEntry()

	if !spoolContainsEntryEndedEvent(t, filepath.Join(dataDir, "logs", "spool")) {
		t.Fatal("the entry hop's own tunnel-ended event never reached the local spool disk before the node shut down")
	}
}

// spoolContainsEntryEndedEvent reads every segment file in dir looking for a tunnel event recorded by the entry
// hop with stage "ended" - the same shape agent.go's spool would contain if TestLogPipelineCapturesFinalTunnelEventOnGracefulShutdown's
// scenario worked correctly, decoded loosely (substring match on the NDJSON) since this test only cares whether
// the record is present at all, not its full shape (which internal/overlay's own tests already cover).
func spoolContainsEntryEndedEvent(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".jsonl.gz") {
			continue
		}
		data := readGzipFile(t, filepath.Join(dir, entry.Name()))
		if strings.Contains(data, `"stage":"ended"`) && strings.Contains(data, `"role":"entry"`) {
			return true
		}
	}
	return false
}

func readGzipFile(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		// A segment mid-write (not yet gzip-finalized) is not this test's concern; treat it as empty.
		return ""
	}
	defer gz.Close()
	data, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

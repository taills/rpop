package agent

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLogPipelineFreshSpoolAfterReRegistrationIsNotTreatedAsAReplay covers the D24/D26 interaction stage 5 step
// 3's design record calls out: a node that loses its local state (data dir and log spool, e.g. reinstalled) and
// re-registers gets a new certificate generation, and the controller must reset its persisted high-water mark to
// 0 for it (southboundRegister) - otherwise the freshly numbered segment 1 the reinstalled spool produces would
// look like a replay of whatever the earlier installation last acknowledged, and be silently dropped instead of
// written.
func TestLogPipelineFreshSpoolAfterReRegistrationIsNotTreatedAsAReplay(t *testing.T) {
	c := startLoggingController(t, nil)
	token := c.createNode("edge-1")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer origin.Close()
	port := freePort(t)
	c.createSite("web", "edge-1", "default", origin.URL, port)
	target := fmt.Sprintf("http://127.0.0.1:%d/", port)

	first, stopFirst := startAgent(t, Config{ControllerURL: c.southbound.URL, JoinToken: token, DataDir: t.TempDir(), Version: "test"})
	eventually(t, "the node serves the site", func() bool { return first.Engine().Running("web") })
	if body := get(t, target); body != "ok" {
		t.Fatalf("body = %q", body)
	}
	waitForAck(t, first, 1)
	stopFirst()

	// Simulate a lost/reinstalled node: a brand new data directory (so a brand new identity and a brand new,
	// empty spool numbering from segment 1 again) registers under the same node id with a freshly issued token.
	reissued := c.reissueToken("edge-1")
	second, _ := startAgent(t, Config{ControllerURL: c.southbound.URL, JoinToken: reissued, DataDir: t.TempDir(), Version: "test"})
	eventually(t, "the re-registered node serves the site again", func() bool { return second.Engine().Running("web") })
	if body := get(t, target); body != "ok" {
		t.Fatalf("body = %q", body)
	}
	waitForAck(t, second, 1)

	result := c.searchSite("web")
	if result.Total != 2 {
		t.Fatalf("access log total = %d, want 2 (one from each registration; the post-reinstall segment 1 must not be dropped as a replay)", result.Total)
	}
}

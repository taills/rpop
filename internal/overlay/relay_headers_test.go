package overlay

import (
	"net/http"
	"testing"

	"github.com/rpop-project/rpop/internal/traceid"
)

// TestTunnelOpenFromRequestValidatesTheTunnelIDHeader covers stage 5 security review item 4: only a
// well-formed, UUID-shaped Rpop-Tunnel-Id combined with Rpop-Tunnel-Log turns into a tunnelOpen that logs
// events; anything else behaves like a request that never asked for logging.
func TestTunnelOpenFromRequestValidatesTheTunnelIDHeader(t *testing.T) {
	tests := []struct {
		name          string
		tunnelID      string
		setLogHeader  bool
		wantLogEvents bool
	}{
		{"a valid uuid with logging requested", traceid.New(), true, true},
		{"a malformed id with logging requested", "../../etc/passwd", true, false},
		{"an empty id with logging requested", "", true, false},
		{"a valid uuid without logging requested", traceid.New(), false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			header := make(http.Header)
			header.Set(TunnelIDHeader, tc.tunnelID)
			if tc.setLogHeader {
				header.Set(TunnelLogHeader, "1")
			}
			open := tunnelOpenFromRequest(&http.Request{Header: header})
			if open.logEvents != tc.wantLogEvents {
				t.Fatalf("logEvents = %v, want %v", open.logEvents, tc.wantLogEvents)
			}
			if open.tunnelID != tc.tunnelID {
				t.Fatalf("tunnelID = %q, want %q (untouched, even when logging is not enabled)", open.tunnelID, tc.tunnelID)
			}
		})
	}
}

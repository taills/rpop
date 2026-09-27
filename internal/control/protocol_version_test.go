package control

import (
	"strings"
	"testing"
)

// TestClassifyProtocolVersionCoversEveryWindowOutcome exercises D27's decision table directly: equal to
// current, behind current but still in the window (accepted, marked outdated), above the ceiling (rejected,
// names the controller), and below the floor (rejected, names the node). The middle case cannot be reached
// through the real southbound.MinSupportedProtocolVersion/ProtocolVersion today — both are 1, since D27 has
// just introduced version numbers and no older one has ever existed to leave a gap below current — so this
// calls classifyProtocolVersion with an arbitrary window instead of those constants (see its own doc comment).
func TestClassifyProtocolVersionCoversEveryWindowOutcome(t *testing.T) {
	const min, current = 1, 3
	tests := []struct {
		name            string
		version         int
		wantAccepted    bool
		wantOutdated    bool
		wantMessagePart string
	}{
		{name: "equal to current is accepted and current", version: 3, wantAccepted: true, wantOutdated: false},
		{name: "behind current but in window is accepted and outdated", version: 2, wantAccepted: true, wantOutdated: true},
		{name: "at the floor is accepted and outdated", version: 1, wantAccepted: true, wantOutdated: true},
		{name: "above the ceiling is rejected naming the controller", version: 4, wantAccepted: false, wantMessagePart: "upgrade the controller"},
		{name: "below the floor is rejected naming the node", version: 0, wantAccepted: false, wantMessagePart: "upgrade the node"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			decision := classifyProtocolVersion(tc.version, min, current)
			if decision.accepted != tc.wantAccepted {
				t.Fatalf("accepted = %v, want %v", decision.accepted, tc.wantAccepted)
			}
			if decision.accepted && decision.outdated != tc.wantOutdated {
				t.Fatalf("outdated = %v, want %v", decision.outdated, tc.wantOutdated)
			}
			if !decision.accepted && tc.wantMessagePart != "" && !strings.Contains(decision.message, tc.wantMessagePart) {
				t.Fatalf("message = %q, want it to contain %q", decision.message, tc.wantMessagePart)
			}
		})
	}
}

// TestNodeRegistryReportProtocolVersionLogsOnlyOnTransition covers nodeRegistry.reportProtocolVersion's half of
// the "warn once, not every call" contract: changed is true only the first time a node's outdated/current status
// flips, not on every repeated report of the same status (checkProtocolVersion relies on this to avoid warning
// on every 15s status heartbeat from a node that stays outdated).
func TestNodeRegistryReportProtocolVersionLogsOnlyOnTransition(t *testing.T) {
	r := newNodeRegistry()
	if changed := r.reportProtocolVersion("node-a", 1, true); !changed {
		t.Fatal("first report of outdated should be a transition")
	}
	if changed := r.reportProtocolVersion("node-a", 1, true); changed {
		t.Fatal("repeated outdated report should not be a transition")
	}
	if changed := r.reportProtocolVersion("node-a", 3, false); !changed {
		t.Fatal("going from outdated to current should be a transition")
	}
	if changed := r.reportProtocolVersion("node-a", 3, false); changed {
		t.Fatal("repeated current report should not be a transition")
	}
	snapshot, ok := r.snapshot("node-a")
	if !ok || snapshot.protocolVersion != 3 || snapshot.protocolStatus != "current" {
		t.Fatalf("snapshot = %#v, ok=%v", snapshot, ok)
	}
}

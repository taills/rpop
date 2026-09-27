package control

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/rpop-project/rpop/internal/dataplane"
	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/southbound"
)

func TestSanitizeStatusLeavesSmallReportsUnchanged(t *testing.T) {
	status := southbound.Status{
		Links: []overlay.LinkStatus{{Peer: "node2", Address: "10.0.0.2:7000", Proxies: []string{"socks5://10.1.0.1:1080"}, Status: "up"}},
		Paths: []dataplane.UpstreamPathHealth{{SiteID: "web", Upstream: "https://example.com", Paths: []dataplane.PathHealth{{Label: "node2", Status: "healthy"}}}},
	}
	sanitized := sanitizeStatus(status)
	if len(sanitized.Links) != 1 || sanitized.Links[0].Peer != "node2" || sanitized.Links[0].Address != "10.0.0.2:7000" ||
		!slices.Equal(sanitized.Links[0].Proxies, []string{"socks5://10.1.0.1:1080"}) {
		t.Fatalf("links = %#v", sanitized.Links)
	}
	if len(sanitized.Paths) != 1 || len(sanitized.Paths[0].Paths) != 1 || sanitized.Paths[0].Paths[0] != status.Paths[0].Paths[0] {
		t.Fatalf("paths = %#v", sanitized.Paths)
	}
}

func TestSanitizeStatusPassesThroughNilHealth(t *testing.T) {
	sanitized := sanitizeStatus(southbound.Status{Version: "v"})
	if sanitized.Links != nil || sanitized.Paths != nil {
		t.Fatalf("sanitizing an empty report produced non-nil health: %#v", sanitized)
	}
}

func TestSanitizeStatusBoundsLinkCountAndStringLengths(t *testing.T) {
	longString := strings.Repeat("e", maxHealthStringBytes+50)
	longProxies := make([]string, maxProxyChainEntries+5)
	for i := range longProxies {
		longProxies[i] = longString
	}
	links := make([]overlay.LinkStatus, maxReportedLinks+10)
	for i := range links {
		links[i] = overlay.LinkStatus{Peer: fmt.Sprintf("peer-%d", i), Address: longString, LastError: longString, Proxies: longProxies}
	}

	sanitized := sanitizeStatus(southbound.Status{Links: links})
	if len(sanitized.Links) != maxReportedLinks {
		t.Fatalf("links = %d, want %d", len(sanitized.Links), maxReportedLinks)
	}
	first := sanitized.Links[0]
	if len(first.Address) != maxHealthStringBytes {
		t.Fatalf("address length = %d, want %d", len(first.Address), maxHealthStringBytes)
	}
	if len(first.LastError) != maxHealthStringBytes {
		t.Fatalf("lastError length = %d, want %d", len(first.LastError), maxHealthStringBytes)
	}
	if len(first.Proxies) != maxProxyChainEntries {
		t.Fatalf("proxies = %d, want %d", len(first.Proxies), maxProxyChainEntries)
	}
	for _, proxy := range first.Proxies {
		if len(proxy) != maxHealthStringBytes {
			t.Fatalf("proxy label length = %d, want %d", len(proxy), maxHealthStringBytes)
		}
	}
}

func TestSanitizeStatusBoundsPathUpstreamAndTotalCounts(t *testing.T) {
	longString := strings.Repeat("e", maxHealthStringBytes+50)
	manyPaths := make([]dataplane.PathHealth, maxReportedPathsPerUp+5)
	for i := range manyPaths {
		manyPaths[i] = dataplane.PathHealth{Label: longString, LastError: longString, Status: "cooling"}
	}
	groups := make([]dataplane.UpstreamPathHealth, maxReportedUpstreams+5)
	for i := range groups {
		groups[i] = dataplane.UpstreamPathHealth{SiteID: longString, Upstream: fmt.Sprintf("u-%d", i), Paths: manyPaths}
	}

	sanitized := sanitizeStatus(southbound.Status{Paths: groups})
	if len(sanitized.Paths) > maxReportedUpstreams {
		t.Fatalf("upstream groups = %d, want at most %d", len(sanitized.Paths), maxReportedUpstreams)
	}
	if len(sanitized.Paths[0].SiteID) != maxHealthStringBytes {
		t.Fatalf("siteId length = %d, want %d", len(sanitized.Paths[0].SiteID), maxHealthStringBytes)
	}
	total := 0
	for _, group := range sanitized.Paths {
		if len(group.Paths) > maxReportedPathsPerUp {
			t.Fatalf("group %q has %d paths, want at most %d", group.Upstream, len(group.Paths), maxReportedPathsPerUp)
		}
		for _, p := range group.Paths {
			if len(p.Label) != maxHealthStringBytes || len(p.LastError) != maxHealthStringBytes {
				t.Fatalf("path %#v was not truncated", p)
			}
		}
		total += len(group.Paths)
	}
	if total > maxReportedPathsTotal {
		t.Fatalf("total paths = %d, want at most %d", total, maxReportedPathsTotal)
	}
}

func TestNodeRegistryReportSanitizesBeforeStoring(t *testing.T) {
	r := newNodeRegistry()
	links := make([]overlay.LinkStatus, maxReportedLinks+1)
	for i := range links {
		links[i] = overlay.LinkStatus{Peer: fmt.Sprintf("peer-%d", i)}
	}
	r.report("node1", southbound.Status{Links: links})
	runtime, ok := r.snapshot("node1")
	if !ok {
		t.Fatal("node1 was not recorded")
	}
	if len(runtime.status.Links) != maxReportedLinks {
		t.Fatalf("stored links = %d, want %d: report did not sanitize before storing", len(runtime.status.Links), maxReportedLinks)
	}
}

// TestSanitizeLinksReplacesOutOfEnumStatusAndTruncatesTimestamps covers the two fields sanitizeLinks used to miss
// (stage 6 Go review): Status is replaced with "unknown" when it is not one of overlay.LinkStatus's own three
// values, and DownUntil/LastSuccess are truncated the same as every other diagnostic string.
func TestSanitizeLinksReplacesOutOfEnumStatusAndTruncatesTimestamps(t *testing.T) {
	longString := strings.Repeat("t", maxHealthStringBytes+50)
	sanitized := sanitizeStatus(southbound.Status{Links: []overlay.LinkStatus{
		{Peer: "a", Status: "up", DownUntil: longString, LastSuccess: longString},
		{Peer: "b", Status: "dialing"},
		{Peer: "c", Status: "down"},
		{Peer: "d", Status: "boot-up-please-trust-me"},
	}}).Links
	if len(sanitized) != 4 {
		t.Fatalf("links = %d, want 4", len(sanitized))
	}
	if len(sanitized[0].DownUntil) != maxHealthStringBytes || len(sanitized[0].LastSuccess) != maxHealthStringBytes {
		t.Fatalf("link 0 = %#v, want DownUntil/LastSuccess truncated to %d", sanitized[0], maxHealthStringBytes)
	}
	wantStatuses := []string{"up", "dialing", "down", "unknown"}
	for i, want := range wantStatuses {
		if sanitized[i].Status != want {
			t.Fatalf("link %d status = %q, want %q", i, sanitized[i].Status, want)
		}
	}
}

// TestSanitizePathsReplacesOutOfEnumStatusAndTruncatesUntil covers dataplane.PathHealth's own equivalent gap:
// Status must be "healthy" or "cooling", and Until is bounded the same as Label/LastError.
func TestSanitizePathsReplacesOutOfEnumStatusAndTruncatesUntil(t *testing.T) {
	longString := strings.Repeat("t", maxHealthStringBytes+50)
	sanitized := sanitizeStatus(southbound.Status{Paths: []dataplane.UpstreamPathHealth{{
		SiteID: "web", Upstream: "https://example.com", Paths: []dataplane.PathHealth{
			{Label: "a", Status: "healthy"},
			{Label: "b", Status: "cooling", Until: longString},
			{Label: "c", Status: "definitely-fine-trust-me"},
		},
	}}}).Paths
	if len(sanitized) != 1 || len(sanitized[0].Paths) != 3 {
		t.Fatalf("paths = %#v", sanitized)
	}
	got := sanitized[0].Paths
	if len(got[1].Until) != maxHealthStringBytes {
		t.Fatalf("path 1 Until length = %d, want %d", len(got[1].Until), maxHealthStringBytes)
	}
	wantStatuses := []string{"healthy", "cooling", "unknown"}
	for i, want := range wantStatuses {
		if got[i].Status != want {
			t.Fatalf("path %d status = %q, want %q", i, got[i].Status, want)
		}
	}
}

// TestSanitizeClockOffsetClampsOutOfRangeValues covers D28's range check: a plausible offset (and the RTT
// reported alongside it) survives untouched, but one beyond maxClockOffsetMillis is dropped together with its
// RTT, since the two only mean anything together.
func TestSanitizeClockOffsetClampsOutOfRangeValues(t *testing.T) {
	ptr := func(v int64) *int64 { return &v }
	tests := []struct {
		name           string
		offset, rtt    *int64
		wantOffset     *int64
		wantRTTDropped bool
	}{
		{"nil offset stays nil", nil, nil, nil, true},
		{"a plausible offset and RTT survive untouched", ptr(1500), ptr(40), ptr(1500), false},
		{"exactly at the boundary survives", ptr(maxClockOffsetMillis), ptr(40), ptr(maxClockOffsetMillis), false},
		{"a negative offset within range survives", ptr(-1500), ptr(40), ptr(-1500), false},
		{"an offset just beyond the boundary is dropped", ptr(maxClockOffsetMillis + 1), ptr(40), nil, true},
		{"a large negative offset is dropped", ptr(-maxClockOffsetMillis - 1), ptr(40), nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOffset, gotRTT := sanitizeClockOffset(tt.offset, tt.rtt)
			if (gotOffset == nil) != (tt.wantOffset == nil) || (gotOffset != nil && *gotOffset != *tt.wantOffset) {
				t.Fatalf("offset = %v, want %v", gotOffset, tt.wantOffset)
			}
			if tt.wantRTTDropped && gotRTT != nil {
				t.Fatalf("rtt = %v, want nil alongside a dropped offset", *gotRTT)
			}
			if !tt.wantRTTDropped && (gotRTT == nil || *gotRTT != *tt.rtt) {
				t.Fatalf("rtt = %v, want %v", gotRTT, tt.rtt)
			}
		})
	}
}

// TestNodeRegistryReportSignalsWhenSanitizeDropsTheClockOffset covers report's second return value, which
// southboundStatus uses to log the drop (D28): it must be true exactly when the node reported an offset that
// sanitizeStatus's range check then removed, and false whenever there was nothing to drop.
func TestNodeRegistryReportSignalsWhenSanitizeDropsTheClockOffset(t *testing.T) {
	r := newNodeRegistry()
	inRange := int64(1000)
	tooLarge := maxClockOffsetMillis + 1
	tests := []struct {
		name    string
		offset  *int64
		wantErr bool
	}{
		{"no offset reported at all", nil, false},
		{"an in-range offset is kept", &inRange, false},
		{"an out-of-range offset is dropped", &tooLarge, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, dropped := r.report("node1", southbound.Status{ClockOffsetMillis: tt.offset})
			if dropped != tt.wantErr {
				t.Fatalf("report() clockOffsetDropped = %v, want %v", dropped, tt.wantErr)
			}
		})
	}
}

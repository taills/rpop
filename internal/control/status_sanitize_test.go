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

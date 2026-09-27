package control

import (
	"github.com/rpop-project/rpop/internal/dataplane"
	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/southbound"
)

// Bounds on the link/path health data a node reports alongside its status (D19, stage 6). A node is a
// half-trusted party: it holds a valid mTLS certificate but could be compromised or buggy, and the controller
// keeps every status report in memory and echoes it back over GET /api/nodes and GET /api/topology. Without a
// bound here, a node could inflate the controller's memory or the size of every future response to those
// endpoints just by reporting an unbounded number of links or paths, or oversized strings, inside an otherwise
// small status payload (southboundStatus already caps the whole request body at maxStatusBody).
const (
	maxReportedLinks      = 512
	maxReportedUpstreams  = 512
	maxReportedPathsTotal = 4096
	maxReportedPathsPerUp = 64
	maxHealthStringBytes  = 256
	// maxProxyChainEntries generously covers any real proxy chain (D13/D18 cap a path at maxPathHops hops), while
	// still bounding a hostile report.
	maxProxyChainEntries = 32
)

// sanitizeStatus bounds the entry counts and string lengths of the link/path health data inside status before
// nodeRegistry.report keeps it or nodeView/topologyAPI serve it back, so a node's own report cannot grow the
// controller's memory or response sizes without bound. It leaves every other field of status untouched.
func sanitizeStatus(status southbound.Status) southbound.Status {
	status.Links = sanitizeLinks(status.Links)
	status.Paths = sanitizePaths(status.Paths)
	return status
}

func sanitizeLinks(links []overlay.LinkStatus) []overlay.LinkStatus {
	if links == nil {
		return nil
	}
	if len(links) > maxReportedLinks {
		links = links[:maxReportedLinks]
	}
	sanitized := make([]overlay.LinkStatus, len(links))
	for i, link := range links {
		link.Peer = truncate(link.Peer, maxHealthStringBytes)
		link.Address = truncate(link.Address, maxHealthStringBytes)
		link.LastError = truncate(link.LastError, maxHealthStringBytes)
		if len(link.Proxies) > maxProxyChainEntries {
			link.Proxies = link.Proxies[:maxProxyChainEntries]
		}
		if link.Proxies != nil {
			proxies := make([]string, len(link.Proxies))
			for j, proxy := range link.Proxies {
				proxies[j] = truncate(proxy, maxHealthStringBytes)
			}
			link.Proxies = proxies
		}
		sanitized[i] = link
	}
	return sanitized
}

func sanitizePaths(groups []dataplane.UpstreamPathHealth) []dataplane.UpstreamPathHealth {
	if groups == nil {
		return nil
	}
	if len(groups) > maxReportedUpstreams {
		groups = groups[:maxReportedUpstreams]
	}
	sanitized := make([]dataplane.UpstreamPathHealth, 0, len(groups))
	total := 0
	for _, group := range groups {
		if total >= maxReportedPathsTotal {
			break
		}
		group.SiteID = truncate(group.SiteID, maxHealthStringBytes)
		group.Upstream = truncate(group.Upstream, maxHealthStringBytes)
		if len(group.Paths) > maxReportedPathsPerUp {
			group.Paths = group.Paths[:maxReportedPathsPerUp]
		}
		if remaining := maxReportedPathsTotal - total; len(group.Paths) > remaining {
			group.Paths = group.Paths[:remaining]
		}
		paths := make([]dataplane.PathHealth, len(group.Paths))
		for i, p := range group.Paths {
			p.Label = truncate(p.Label, maxHealthStringBytes)
			p.LastError = truncate(p.LastError, maxHealthStringBytes)
			paths[i] = p
		}
		group.Paths = paths
		total += len(paths)
		sanitized = append(sanitized, group)
	}
	return sanitized
}

// truncate cuts s to at most max bytes. Health strings are diagnostic text (dial errors, path labels), not
// content where a byte-level cut on a multi-byte rune matters.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

package sharedport

// This file exposes, read-only, the pure admission and hostname-overlap rules PutSite/PutPlaintextOwner already
// enforce for real, so a caller that wants to know "would this be admitted?" before anything is actually saved
// or bound does not have to re-derive the rule and risk it drifting from the real one. internal/control's
// pre-save validation (see docs/architecture/control-data-plane.md §5, "共享端口(第三段)") is the first user:
// it predicts, at site-save time, the same error PutSite/PutPlaintextOwner would return for real once the site
// actually starts, without registering (let alone binding) anything.

// HostnamesOverlap reports whether hostname patterns a and b (already normalized, see NormalizeHostnames) would
// match at least one of the same names — the same rule siteTable.admit and checkPlainOwnerOverlap/
// checkOwnerSiteOverlap use internally to reject a hostname that shadows another owner's or site's, exposed for
// a caller that needs the same verdict without an admitted siteTable at hand (e.g. comparing a candidate
// hostname directly against -console-hostnames or a specific SNI name).
func HostnamesOverlap(a, b string) bool {
	return hostPatternsOverlap(a, b)
}

// PredictedSiteSet is a throwaway, in-memory simulation of one address's plaintext- or TLS-site table: enough to
// ask "could a site with this ID and these hostnames be admitted alongside what's already here?" (see Admit)
// without a real Registry, a real bound listener, or any of a real SiteRoute's Handler/Certificate plumbing.
type PredictedSiteSet struct {
	table *siteTable
}

// NewPredictedSiteSet builds a PredictedSiteSet from the sites already believed to occupy an address: each
// entry's key is a site ID, its value that site's normalized hostnames (see NormalizeHostnames), nil/empty
// meaning that site has none. The caller decides which real sites belong here (same normalized address, same
// encoding, same node/process — none of which this package knows anything about).
func NewPredictedSiteSet(existing map[string][]string) *PredictedSiteSet {
	table := newSiteTable()
	for id, hostnames := range existing {
		table = table.put(SiteRoute{ID: id, Hostnames: hostnames})
	}
	return &PredictedSiteSet{table: table}
}

// Admit reports the same error PutSite would return for real if a site with id and hostnames tried to join this
// set alongside hasOwner (a plaintext console for a plaintext prediction, or any TLS owner/default TLS owner for
// a TLS one — PutSite's own hasOwner rule, see its doc comment).
func (s *PredictedSiteSet) Admit(id string, hostnames []string, hasOwner bool) error {
	return s.table.admit(id, hostnames, hasOwner, nil)
}

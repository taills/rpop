package sharedport

import (
	"crypto/tls"
	"fmt"
	"net/http"
)

// SiteRoute describes one site's claim on a shared address. Hostnames must be non-empty whenever the site shares
// its address with anything else (another site or an owner) — see Registry.PutSite — and normalized (see
// NormalizeHostnames); a lone site on an address may still leave it empty, exactly as before this package
// existed.
type SiteRoute struct {
	// ID identifies the site across Put/Remove calls; a second PutSite with the same ID replaces the first.
	ID        string
	Hostnames []string
	TLS       bool
	// Certificate returns the site's current certificate. Required when TLS is set; called again on every TLS
	// handshake for one of this route's hostnames, so rotating the certificate a caller's own atomic pointer
	// backs needs no re-registration. Nil when TLS is false.
	Certificate func() *tls.Certificate
	// Handler serves the site's traffic once a request is routed to it.
	Handler http.Handler
}

// siteTable indexes a port's sites of one encoding (plaintext or TLS) by ID and by hostname, mirroring the
// hostname/wildcard matching and "lone site with no hostname serves everything" fallback that already existed
// before sites were routed through a shared registry.
type siteTable struct {
	byID   map[string]SiteRoute
	byHost map[string]SiteRoute
}

func newSiteTable() *siteTable {
	return &siteTable{byID: map[string]SiteRoute{}, byHost: map[string]SiteRoute{}}
}

// put returns a new siteTable with route installed, replacing any existing route of the same ID. The receiver is
// never mutated (copy-on-write; see routeTable).
func (t *siteTable) put(route SiteRoute) *siteTable {
	next := &siteTable{byID: make(map[string]SiteRoute, len(t.byID)+1), byHost: make(map[string]SiteRoute, len(t.byHost)+len(route.Hostnames))}
	for id, r := range t.byID {
		if id == route.ID {
			continue
		}
		next.byID[id] = r
		for _, h := range r.Hostnames {
			next.byHost[h] = r
		}
	}
	next.byID[route.ID] = route
	for _, h := range route.Hostnames {
		next.byHost[h] = route
	}
	return next
}

// remove returns a new siteTable without id (copy-on-write).
func (t *siteTable) remove(id string) *siteTable {
	if _, ok := t.byID[id]; !ok {
		return t
	}
	next := newSiteTable()
	for otherID, r := range t.byID {
		if otherID == id {
			continue
		}
		next.byID[otherID] = r
		for _, h := range r.Hostnames {
			next.byHost[h] = r
		}
	}
	return next
}

func (t *siteTable) len() int { return len(t.byID) }

// findByHost looks up a route by exact hostname, then by the longest matching "*.suffix" wildcard.
func (t *siteTable) findByHost(host string) (SiteRoute, bool) {
	if host == "" {
		return SiteRoute{}, false
	}
	if route, ok := t.byHost[host]; ok {
		return route, true
	}
	var found SiteRoute
	longest := 0
	matched := false
	for pattern, route := range t.byHost {
		if len(pattern) > 2 && pattern[0] == '*' && pattern[1] == '.' {
			suffix := pattern[1:]
			if len(host) > len(suffix) && hasSuffix(host, suffix) && len(suffix) > longest {
				found, longest, matched = route, len(suffix), true
			}
		}
	}
	return found, matched
}

func hasSuffix(host, suffix string) bool {
	return len(host) >= len(suffix) && host[len(host)-len(suffix):] == suffix
}

// matchesHost reports whether host would resolve to a route in this table: either an exact/wildcard hostname
// match (findByHost), or, when it does not, the table's lone hostname-less route (only). Used by
// getConfigForClient to decide whether an unmatched-by-owner TLS connection belongs to this port's shared
// TLS-sites config before falling further back to a default owner (see Registry.PutDefaultTLSOwner) — kept
// separate from tlsSiteConfig's own GetCertificate (which needs the route itself, not just whether one exists)
// so both stay in sync without one calling the other.
func (t *siteTable) matchesHost(host string) bool {
	if _, ok := t.findByHost(host); ok {
		return true
	}
	_, ok := t.only()
	return ok
}

// only returns the table's sole route, if it has exactly one and that route was registered with no hostname at
// all — exactly as it did before sites shared a registry (a lone hostname-less site on an address caught every
// request, since there was nothing to disambiguate by). A route that does have a configured hostname never
// matches here even when it is the table's only one, so an unmatched Host/SNI correctly falls through to an
// owner (or a 404/misdirected response) instead of being swallowed by a site whose hostname it does not match.
func (t *siteTable) only() (SiteRoute, bool) {
	if len(t.byID) != 1 {
		return SiteRoute{}, false
	}
	for _, route := range t.byID {
		if len(route.Hostnames) != 0 {
			return SiteRoute{}, false
		}
		return route, true
	}
	return SiteRoute{}, false
}

// admit reports whether a site with id and hostnames may join this table alongside an owner (hasOwner) and
// hostCount other already-admitted routes not in ignore, and alongside the routes already in the table.
// ignore names sites that are mid-move to another address within the same snapshot Apply (see dataplane's
// leaving set) and so must not count as "already sharing" this address.
func (t *siteTable) admit(id string, hostnames []string, hasOwner bool, ignore map[string]bool) error {
	others := 0
	for otherID, existing := range t.byID {
		if otherID == id || ignore[otherID] {
			continue
		}
		others++
		if len(hostnames) > 0 && len(existing.Hostnames) == 0 {
			return fmt.Errorf("已有站点 %s 未配置 hostname; 请先为它配置 hostname 再共用监听地址", existing.ID)
		}
	}
	if len(hostnames) == 0 && (others > 0 || hasOwner) {
		return fmt.Errorf("与其他站点或控制台/中继共用监听地址时必须配置 hostname")
	}
	for _, hostname := range hostnames {
		for existingHost, existing := range t.byHost {
			if existing.ID != id && !ignore[existing.ID] && hostPatternsOverlap(hostname, existingHost) {
				return fmt.Errorf("hostname %q 与站点 %s 已使用的 hostname 重叠", hostname, existing.ID)
			}
		}
	}
	return nil
}

// plaintextOwner is the fallback handler for plaintext requests a port's sites do not claim.
type plaintextOwner struct {
	handler   http.Handler
	hostnames map[string]bool // nil/empty means "answer every Host the sites did not claim"
}

func (o *plaintextOwner) claims(host string) bool {
	if o == nil {
		return false
	}
	if len(o.hostnames) == 0 {
		return true
	}
	return o.hostnames[host]
}

// checkPlainOwnerOverlap rejects a plaintext site hostname that overlaps a restricted plaintext owner's
// hostname list (see Registry.PutPlaintextOwner / -console-hostnames): dispatchPlain always prefers a matching
// site over the owner, so letting both claim the same Host would silently starve the owner of it. An
// unrestricted owner (nil hostnames, the default) claims no specific name of its own and cannot overlap.
func checkPlainOwnerOverlap(owner *plaintextOwner, siteID string, hostnames []string) error {
	if owner == nil || len(owner.hostnames) == 0 {
		return nil
	}
	for _, h := range hostnames {
		for ownerHost := range owner.hostnames {
			if hostPatternsOverlap(h, ownerHost) {
				return fmt.Errorf("站点 %s 的 hostname %q 与控制台 hostname %q 重叠,请改用不同的 hostname", siteID, h, ownerHost)
			}
		}
	}
	return nil
}

// checkOwnerSiteOverlap is checkPlainOwnerOverlap's mirror image, run when an owner (not a site) is the one being
// registered or given a new hostname restriction: it rejects any of ownerHostnames that overlaps a hostname an
// already-registered plaintext site claims.
func checkOwnerSiteOverlap(sites *siteTable, ownerHostnames []string) error {
	for _, ownerHost := range ownerHostnames {
		for siteHost, route := range sites.byHost {
			if hostPatternsOverlap(ownerHost, siteHost) {
				return fmt.Errorf("控制台 hostname %q 与站点 %s 已使用的 hostname %q 重叠,请改用不同的 hostname", ownerHost, route.ID, siteHost)
			}
		}
	}
	return nil
}

// tlsOwnerEntry is a TLS owner's tls.Config and the virtual listener its own *http.Server drives.
type tlsOwnerEntry struct {
	config   *tls.Config
	listener *virtualListener
}

// routeTable is one port's complete routing state, replaced wholesale (copy-on-write) on every registration
// change and read without a lock from atomic.Pointer by the connection-dispatch hot path (see dispatch.go).
type routeTable struct {
	plainOwner *plaintextOwner
	tlsOwners  map[string]*tlsOwnerEntry // key: exact SNI
	// defaultOwner is this address's fallback for a TLS connection whose SNI (including no SNI at all) matched
	// neither an exact tlsOwners entry nor a tlsSites hostname; see Registry.PutDefaultTLSOwner and
	// dispatch.go's getConfigForClient for the match order. At most one per address.
	defaultOwner *tlsOwnerEntry
	plainSites   *siteTable
	tlsSites     *siteTable
}

func emptyRouteTable() *routeTable {
	return &routeTable{tlsOwners: map[string]*tlsOwnerEntry{}, plainSites: newSiteTable(), tlsSites: newSiteTable()}
}

// clone shallow-copies the table so a caller can replace exactly one field and Store the result, without racing
// a concurrent reader that is still using the previous table's other fields (which it does not mutate, so
// sharing them is safe).
func (t *routeTable) clone() *routeTable {
	owners := make(map[string]*tlsOwnerEntry, len(t.tlsOwners))
	for k, v := range t.tlsOwners {
		owners[k] = v
	}
	return &routeTable{plainOwner: t.plainOwner, tlsOwners: owners, defaultOwner: t.defaultOwner, plainSites: t.plainSites, tlsSites: t.tlsSites}
}

// occupants counts how many owners and sites this table has, for the port's refcounted lifecycle.
func (t *routeTable) occupants() int {
	n := t.plainSites.len() + t.tlsSites.len() + len(t.tlsOwners)
	if t.plainOwner != nil {
		n++
	}
	if t.defaultOwner != nil {
		n++
	}
	return n
}

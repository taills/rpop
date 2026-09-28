package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/sharedport"
	"github.com/rpop-project/rpop/internal/store"
)

// validateSharedPortPlacement pre-validates a site's listen address/hostname configuration against every address
// this controller already knows about — the console, southbound, the relay port of every node the site is
// placed on, and any other site placed on the same node — so a configuration that would fail for real once
// internal/sharedport or internal/dataplane.Engine actually admits it (see PutSite/PutPlaintextOwner/
// buildRuntime) is instead rejected here, synchronously, in clear Chinese, before it is even saved. See
// docs/architecture/control-data-plane.md §5, "共享端口(第三段)" for the rules this mirrors and why.
//
// Called with c.opMu already held, like validateNodeReferences and friends (see the sites/site handlers and
// startLocked): it reads c.bootstrapConsoleAddr/c.bootstrapConsoleHostnames/c.bootstrapSouthboundAddr directly,
// with no locking of its own, for the same reason clockSkewWarnThresholdMillis had to become its own atomic —
// sync.Mutex is not reentrant.
//
// This is necessarily best-effort for a node's relay address: a node started with -relay-listen overrides the
// address the controller computes from its relayAddress (see relayListenAddress), and the controller has no way
// to know that locally-set override. That case remains the node's own job to catch, and it reports it back
// through node.errors like any other apply failure — see docs/architecture/control-data-plane.md §5.
//
// compareSiblings selects whether sibling *sites* placed on the same node are compared against at all (owner
// checks — console/southbound/a node's relay port — always run regardless, since those addresses are occupied
// whether or not this site ever starts). A saved-but-never-started site is not a real collision surface: only
// c.desired holds the sites publishLocked actually registers (see desiredSiblingSitesLocked), so a caller that is
// not about to make this site desired must pass false, or an unrelated, still-unstarted sibling saved earlier at
// the same address (a legitimate "several configs parked on one port, started one at a time" setup) would wrongly
// block this save. Callers: POST /api/sites always passes false (a new site is never auto-started); PUT
// /api/sites/{id} passes whether the site is already in c.desired (it only auto-restarts in that case, see
// control.go's site handler); startLocked always passes true (starting — or restarting — is exactly what is
// about to make it desired).
func (c *Control) validateSharedPortPlacement(ctx context.Context, site store.Site, compareSiblings bool) error {
	hostnames, err := sharedport.NormalizeHostnames(site.Config.Hostnames)
	if err != nil {
		return err
	}
	for _, hostname := range hostnames {
		if pki.IsInternalHostname(hostname) {
			return fmt.Errorf("hostname %q 是内部保留名(控制器或节点专用),请改用其他 hostname", hostname)
		}
	}
	siteAddr := net.JoinHostPort(site.Config.ListenAddress, strconv.Itoa(site.Config.ListenPort))
	if _, _, err := sharedport.NormalizeAddress(siteAddr); err != nil {
		return fmt.Errorf("listenAddress/listenPort: %w", err)
	}

	var siblings []store.Site
	if compareSiblings {
		if siblings, err = c.desiredSiblingSitesLocked(ctx, site.ID); err != nil {
			return err
		}
	}
	for _, nodeID := range siteNodes(site.Config) {
		if err := c.validateSharedPortForNodeLocked(ctx, site, hostnames, siteAddr, nodeID, siblings); err != nil {
			return err
		}
	}
	return nil
}

// desiredSiblingSitesLocked fetches every currently-desired site other than excludeID: exactly the sites
// publishLocked will (re)register for real (see publish.go), and so the only sites a placement can actually
// collide with. Fetched by ID rather than c.store.List so this pre-save check never has to read the whole site
// table while c.opMu is held (it runs on every site save, not just on start).
func (c *Control) desiredSiblingSitesLocked(ctx context.Context, excludeID string) ([]store.Site, error) {
	var siblings []store.Site
	for id := range c.desired {
		if id == excludeID {
			continue
		}
		sibling, err := c.store.Get(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue // a desired site vanishing mid-validation is impossible under opMu; skip defensively
			}
			return nil, err
		}
		siblings = append(siblings, sibling)
	}
	return siblings, nil
}

// sharedPortOwner is a known, fixed-role occupant of a shared address: the console (always plaintext) or a
// TLS-only owner (southbound, or a node's own relay port — both register an exact-SNI owner, and southbound
// additionally registers a default one, see startSouthbound and internal/overlay's relay.go; either way, "some
// TLS owner is here" is all admission cares about, see sharedport.PutSite's hasOwner rule).
type sharedPortOwner struct {
	label             string
	addr              string
	tls               bool
	restrictHostnames []string // console's -console-hostnames, normalized; empty means unrestricted
	// addrIsAuthoritative is false only for a node's relay port: addr is relayListenAddress(node.RelayAddress),
	// always a wildcard bind (":port") guessed from the address other nodes dial, not necessarily the address
	// the node actually binds — an operator's local -relay-listen overrides that bind to any address, including
	// one that exactly matches a site sharing its port (see TestSiteSharesItsNodesOwnRelayPort/
	// TestTLSSiteSharesItsNodesOwnRelayPort in internal/agent, which do exactly this), and the controller has no
	// way to know. A same-port-but-different-scope mismatch against such a guess is therefore not trustworthy
	// enough to hard-reject a save over — see the "Conflicting" case below, which only does that when addr is
	// authoritative (the console's and southbound's own listen addresses, which no node-local flag can move).
	addrIsAuthoritative bool
}

// validateSharedPortForNodeLocked runs validateSharedPortPlacement's checks for one of the site's placement
// nodes. nodeID is either LocalNodeID (the embedded node — console/southbound share its process) or a
// registered node's ID (only its own relay port shares its process; see internal/agent.Agent's private registry).
// siblings is whatever validateSharedPortPlacement's compareSiblings decided to pass: either every other
// currently-desired site (desiredSiblingSitesLocked), or nil when this save is not comparing siblings at all.
func (c *Control) validateSharedPortForNodeLocked(ctx context.Context, site store.Site, hostnames []string, siteAddr, nodeID string, siblings []store.Site) error {
	var owners []sharedPortOwner
	if nodeID == LocalNodeID {
		if c.bootstrapConsoleAddr != "" {
			owners = append(owners, sharedPortOwner{label: "控制台", addr: c.bootstrapConsoleAddr, restrictHostnames: c.bootstrapConsoleHostnames, addrIsAuthoritative: true})
		}
		if c.bootstrapSouthboundAddr != "" {
			owners = append(owners, sharedPortOwner{label: "southbound", addr: c.bootstrapSouthboundAddr, tls: true, addrIsAuthoritative: true})
		}
		// The embedded node's own relay port is never a real collision surface: validatePaths rejects
		// hop.Node == LocalNodeID outright ("the embedded node cannot relay; only registered nodes can"), so the
		// embedded node can never be a hop on any path and its relay port never binds (see
		// internal/overlay.Overlay.applyRelayLocked, which only starts a relay listener once a snapshot's Relay
		// is non-empty). Nothing to add here unless that restriction is ever lifted.
	} else {
		node, err := c.store.GetNode(ctx, nodeID)
		if err != nil {
			if errors.Is(err, store.ErrNodeNotFound) {
				// validateNodeReferences (called alongside this) already reports the missing node.
				return nil
			}
			return err
		}
		if relayListen := relayListenAddress(node.RelayAddress); relayListen != "" {
			owners = append(owners, sharedPortOwner{label: fmt.Sprintf("节点 %s 的中继端口", nodeID), addr: relayListen, tls: true})
		}
	}

	var sameAddressOwners []sharedPortOwner
	for _, owner := range owners {
		switch relation, err := sharedport.Classify(siteAddr, owner.addr); {
		case err != nil:
			continue // an internal address should always be valid; ignore defensively rather than block the save
		case relation == sharedport.Conflicting:
			if !owner.addrIsAuthoritative {
				continue // see sharedPortOwner.addrIsAuthoritative's doc comment: -relay-listen may resolve this
			}
			return sharedport.ConflictError(siteAddr, "站点 "+site.ID, owner.addr, owner.label)
		case relation == sharedport.Same:
			sameAddressOwners = append(sameAddressOwners, owner)
		}
	}

	siblingHostnames := map[string][]string{}
	for _, other := range siblings {
		if other.ID == site.ID || !slices.Contains(siteNodes(other.Config), nodeID) {
			continue
		}
		otherAddr := net.JoinHostPort(other.Config.ListenAddress, strconv.Itoa(other.Config.ListenPort))
		switch relation, err := sharedport.Classify(siteAddr, otherAddr); {
		case err != nil:
			continue // an already-saved site's own address was valid when it was saved; ignore defensively
		case relation == sharedport.Conflicting:
			return sharedport.ConflictError(siteAddr, "站点 "+site.ID, otherAddr, "站点 "+other.ID)
		case relation == sharedport.Same && other.Config.TLS == site.Config.TLS:
			otherHostnames, err := sharedport.NormalizeHostnames(other.Config.Hostnames)
			if err != nil {
				otherHostnames = other.Config.Hostnames // already saved; best effort if it somehow predates validation
			}
			siblingHostnames[other.ID] = otherHostnames
		}
	}

	hasOwner := false
	for _, owner := range sameAddressOwners {
		if owner.tls == site.Config.TLS {
			hasOwner = true
		}
	}
	if err := sharedport.NewPredictedSiteSet(siblingHostnames).Admit(site.ID, hostnames, hasOwner); err != nil {
		return fmt.Errorf("放置节点 %s: %w", nodeLabel(nodeID), err)
	}

	if !site.Config.TLS {
		for _, owner := range sameAddressOwners {
			if owner.tls || len(owner.restrictHostnames) == 0 {
				continue
			}
			for _, hostname := range hostnames {
				for _, restricted := range owner.restrictHostnames {
					if sharedport.HostnamesOverlap(hostname, restricted) {
						return fmt.Errorf("hostname %q 与控制台 -console-hostnames 限定的 %q 重叠,请改用不同的 hostname", hostname, restricted)
					}
				}
			}
		}
	}
	return nil
}

func nodeLabel(id string) string {
	if id == LocalNodeID {
		return "内嵌节点"
	}
	return id
}

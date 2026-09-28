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
func (c *Control) validateSharedPortPlacement(ctx context.Context, site store.Site) error {
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

	sites, err := c.store.List(ctx)
	if err != nil {
		return err
	}
	for _, nodeID := range siteNodes(site.Config) {
		if err := c.validateSharedPortForNodeLocked(ctx, site, hostnames, siteAddr, nodeID, sites); err != nil {
			return err
		}
	}
	return nil
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
func (c *Control) validateSharedPortForNodeLocked(ctx context.Context, site store.Site, hostnames []string, siteAddr, nodeID string, sites []store.Site) error {
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
	for _, other := range sites {
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

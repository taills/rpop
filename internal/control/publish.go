package control

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"sync"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/snapshot"
	"github.com/rpop-project/rpop/internal/store"
)

// LocalNodeID names the node embedded in the controller process; sites without placement run there.
const LocalNodeID = "local"

const revisionSettingKey = "config_revision"

// nodeIDPattern keeps node IDs usable as a DNS label, which node certificates embed.
var nodeIDPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// publication holds the latest snapshot rendered for every node.
type publication struct {
	mu        sync.RWMutex
	revision  int64
	snapshots map[string]snapshot.Snapshot
	// lastGood is the most recent spec and placement of each desired site that resolved successfully. Sites a
	// publication does not resolve again, and sites whose configuration no longer resolves, are published with it.
	lastGood map[string]publishedSite
	// changed is closed and replaced whenever something watch streams must look at again was published.
	changed chan struct{}
}

func newPublication() *publication {
	return &publication{snapshots: make(map[string]snapshot.Snapshot), lastGood: make(map[string]publishedSite), changed: make(chan struct{})}
}

// publishedSite is a resolved site, the nodes it is placed on, and the relay routes its paths need.
type publishedSite struct {
	spec   snapshot.Site
	nodes  []string
	routes []plannedRoute
}

// publishScope says which desired sites a publication resolves from the store again; the others keep the spec
// they were last published with, so starting or stopping one site resolves one site, not every running one.
type publishScope struct {
	// all re-resolves every desired site, for shared settings that any site may reference.
	all bool
	// sites are re-resolved and get fresh runtimes on the embedded node.
	sites []string
}

func (s publishScope) includes(id string) bool {
	return s.all || slices.Contains(s.sites, id)
}

// published reports whether a site is part of the latest publication.
func (p *publication) published(id string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.lastGood[id]
	return ok
}

// watch returns the latest snapshot of a node and a channel that is closed when there may be a newer one.
func (p *publication) watch(nodeID string) (snapshot.Snapshot, bool, <-chan struct{}) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.snapshots[nodeID]
	return s, ok, p.changed
}

// wake makes every watch stream look at the publication again.
func (p *publication) wake() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.wakeLocked()
}

func (p *publication) wakeLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// Snapshot returns the latest snapshot of a node; ok is false before anything was published for it.
func (p *publication) Snapshot(nodeID string) (snapshot.Snapshot, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.snapshots[nodeID]
	return s, ok
}

// Revision returns the latest published revision.
func (p *publication) Revision() int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.revision
}

// siteNodes lists the nodes a site is placed on.
func siteNodes(cfg store.Config) []string {
	if len(cfg.Nodes) == 0 {
		return []string{LocalNodeID}
	}
	return cfg.Nodes
}

func validatePlacement(nodes []string) error {
	seen := make(map[string]bool, len(nodes))
	for _, id := range nodes {
		if !nodeIDPattern.MatchString(id) {
			return fmt.Errorf("invalid node id %q", id)
		}
		if seen[id] {
			return fmt.Errorf("node %q is listed more than once", id)
		}
		seen[id] = true
	}
	return nil
}

func (c *Control) loadRevision(ctx context.Context) error {
	raw, err := c.store.GetSetting(ctx, revisionSettingKey)
	if errors.Is(err, store.ErrSettingNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	revision, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return fmt.Errorf("decode config revision: %w", err)
	}
	c.published.mu.Lock()
	c.published.revision = revision
	c.published.mu.Unlock()
	return nil
}

// publishLocked renders a new revision for every node and applies the local node's snapshot. It returns the
// resolution errors of the sites in scope and the embedded node's per-site apply errors.
func (c *Control) publishLocked(ctx context.Context, scope publishScope) map[string]error {
	errs := make(map[string]error)
	c.published.mu.RLock()
	lastGood := c.published.lastGood
	c.published.mu.RUnlock()
	entries := make(map[string]publishedSite, len(c.desired))
	for id := range c.desired {
		previous, hasPrevious := lastGood[id]
		if !scope.includes(id) {
			if hasPrevious {
				entries[id] = previous
			}
			continue
		}
		entry, err := c.resolvePublished(ctx, id)
		switch {
		case errors.Is(err, errSiteDeleted):
			delete(c.desired, id)
		case err != nil:
			errs[id] = err
			if hasPrevious {
				entries[id] = previous
			}
		default:
			entries[id] = entry
		}
	}

	nodes := c.knownNodesLocked(ctx)
	c.published.mu.Lock()
	c.published.revision++
	revision := c.published.revision
	snapshots := make(map[string]snapshot.Snapshot, len(nodes))
	for nodeID := range nodes {
		snapshots[nodeID] = snapshot.Snapshot{Revision: revision, NodeID: nodeID, Sites: []snapshot.Site{}}
	}
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		for _, nodeID := range entries[id].nodes {
			if s, ok := snapshots[nodeID]; ok {
				s.Sites = append(s.Sites, entries[id].spec)
				snapshots[nodeID] = s
			}
		}
	}
	renderOverlay(snapshots, entries, nodes)
	c.published.snapshots = snapshots
	c.published.lastGood = entries
	c.published.wakeLocked()
	c.published.mu.Unlock()

	if err := c.store.SetSetting(ctx, revisionSettingKey, []byte(strconv.FormatInt(revision, 10))); err != nil {
		c.log.Error("persist config revision", zap.Int64("revision", revision), zap.Error(err))
	}
	localErrors := make(map[string]string)
	if err := c.applyLocalOverlayLocked(ctx, snapshots[LocalNodeID]); err != nil {
		c.log.Error("the embedded node cannot reach other nodes; its paths through them fail over", zap.Error(err))
	}
	for id, err := range c.engine.Apply(snapshots[LocalNodeID].Sites, scope.sites...) {
		errs[id] = err
		localErrors[id] = err.Error()
	}
	c.localRevision, c.localErrors = revision, localErrors
	return errs
}

// errSiteDeleted distinguishes a deleted site from a missing secret, which also reports store.ErrNotFound.
var errSiteDeleted = errors.New("site was deleted")

func (c *Control) resolvePublished(ctx context.Context, id string) (publishedSite, error) {
	site, err := c.store.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return publishedSite{}, errSiteDeleted
	}
	if err != nil {
		return publishedSite{}, err
	}
	spec, routes, err := c.resolveSite(ctx, site)
	if err != nil {
		return publishedSite{}, err
	}
	return publishedSite{spec: spec, nodes: siteNodes(site.Config), routes: routes}, nil
}

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
	// lastGood is the most recent spec of each running site that resolved successfully. A site whose current
	// configuration cannot be resolved keeps being published with it.
	lastGood map[string]snapshot.Site
	// changed is closed and replaced whenever something watch streams must look at again was published.
	changed chan struct{}
}

func newPublication() *publication {
	return &publication{snapshots: make(map[string]snapshot.Snapshot), lastGood: make(map[string]snapshot.Site), changed: make(chan struct{})}
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
// local per-site errors; resolution errors are reported for every site that could not be rendered.
func (c *Control) publishLocked(ctx context.Context, rebuild ...string) map[string]error {
	errs := make(map[string]error)
	bySite := make(map[string]snapshot.Site, len(c.desired))
	placement := make(map[string][]string, len(c.desired))
	c.published.mu.RLock()
	lastGood := c.published.lastGood
	c.published.mu.RUnlock()
	nextGood := make(map[string]snapshot.Site, len(c.desired))
	for id := range c.desired {
		site, err := c.store.Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			delete(c.desired, id)
			continue
		}
		if err != nil {
			errs[id] = err
			if previous, ok := lastGood[id]; ok {
				bySite[id], nextGood[id] = previous, previous
			}
			continue
		}
		placement[id] = siteNodes(site.Config)
		spec, err := c.resolveSite(ctx, site)
		if err != nil {
			errs[id] = err
			previous, ok := lastGood[id]
			if !ok {
				continue
			}
			spec = previous
		}
		bySite[id], nextGood[id] = spec, spec
	}

	nodes := c.knownNodesLocked(ctx)
	c.published.mu.Lock()
	c.published.revision++
	revision := c.published.revision
	snapshots := make(map[string]snapshot.Snapshot, len(nodes))
	for _, nodeID := range nodes {
		snapshots[nodeID] = snapshot.Snapshot{Revision: revision, NodeID: nodeID, Sites: []snapshot.Site{}}
	}
	ids := make([]string, 0, len(bySite))
	for id := range bySite {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		for _, nodeID := range placement[id] {
			if s, ok := snapshots[nodeID]; ok {
				s.Sites = append(s.Sites, bySite[id])
				snapshots[nodeID] = s
			}
		}
	}
	c.published.snapshots = snapshots
	c.published.lastGood = nextGood
	c.published.wakeLocked()
	c.published.mu.Unlock()

	if err := c.store.SetSetting(ctx, revisionSettingKey, []byte(strconv.FormatInt(revision, 10))); err != nil {
		c.log.Error("persist config revision", zap.Int64("revision", revision), zap.Error(err))
	}
	localErrors := make(map[string]string)
	for id, err := range c.engine.Apply(snapshots[LocalNodeID].Sites, rebuild...) {
		errs[id] = err
		localErrors[id] = err.Error()
	}
	c.localRevision, c.localErrors = revision, localErrors
	return errs
}

package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/dataplane"
	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/southbound"
	"github.com/rpop-project/rpop/internal/store"
)

const (
	caSettingKey     = "internal_ca"
	joinTokenTTL     = 24 * time.Hour
	maxNodeNameBytes = 128
)

// nodeRuntime is what the controller knows about a node from its connections.
type nodeRuntime struct {
	status   southbound.Status
	lastSeen time.Time
	streams  int
}

// nodeRegistry tracks live node connections and reports.
type nodeRegistry struct {
	mu    sync.Mutex
	nodes map[string]*nodeRuntime
}

func newNodeRegistry() *nodeRegistry {
	return &nodeRegistry{nodes: make(map[string]*nodeRuntime)}
}

func (r *nodeRegistry) get(id string) *nodeRuntime {
	runtime := r.nodes[id]
	if runtime == nil {
		runtime = &nodeRuntime{}
		r.nodes[id] = runtime
	}
	return runtime
}

func (r *nodeRegistry) connected(id string, delta int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if delta < 0 && r.nodes[id] == nil {
		// The node was deleted while its stream was open.
		return
	}
	runtime := r.get(id)
	runtime.streams = max(runtime.streams+delta, 0)
	runtime.lastSeen = time.Now()
}

// report stores status and returns the LogStats the node reported last time (nil the first time), so a caller
// can log when the node's drop counters increase (D25) without a separate, racy read-then-write.
func (r *nodeRegistry) report(id string, status southbound.Status) *southbound.LogStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	runtime := r.get(id)
	previous := runtime.status.Logs
	runtime.status, runtime.lastSeen = status, time.Now()
	return previous
}

func (r *nodeRegistry) snapshot(id string) (nodeRuntime, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	runtime, ok := r.nodes[id]
	if !ok {
		return nodeRuntime{}, false
	}
	return *runtime, true
}

func (r *nodeRegistry) forget(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.nodes, id)
}

// ensureCA loads the internal CA, creating and persisting it on first use.
func (c *Control) ensureCA(ctx context.Context) (*pki.CA, error) {
	c.caMu.Lock()
	defer c.caMu.Unlock()
	if c.ca != nil {
		return c.ca, nil
	}
	var stored struct {
		CertPEM string `json:"certPem"`
		KeyPEM  string `json:"keyPem"`
	}
	raw, err := c.store.GetSetting(ctx, caSettingKey)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &stored); err != nil {
			return nil, fmt.Errorf("decode internal CA: %w", err)
		}
		ca, err := pki.LoadCA(stored.CertPEM, stored.KeyPEM)
		if err != nil {
			return nil, fmt.Errorf("load internal CA: %w", err)
		}
		c.ca = ca
	case errors.Is(err, store.ErrSettingNotFound):
		ca, err := pki.NewCA("rpop internal CA")
		if err != nil {
			return nil, err
		}
		stored.CertPEM, stored.KeyPEM = ca.CertPEM, ca.KeyPEM
		data, err := json.Marshal(stored)
		if err != nil {
			return nil, err
		}
		created, err := c.store.SetSettingIfAbsent(ctx, caSettingKey, data)
		if err != nil {
			return nil, err
		}
		if !created {
			return nil, errors.New("internal CA was created concurrently; retry")
		}
		c.ca = ca
	default:
		return nil, err
	}
	return c.ca, nil
}

// SetEmbeddedNode chooses whether the controller process also runs the "local" data-plane node.
func (c *Control) SetEmbeddedNode(enabled bool) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.embedded = enabled
}

// knownNodesLocked returns the nodes that receive snapshots: the embedded node and every provisioned node.
func (c *Control) knownNodesLocked(ctx context.Context) map[string]store.Node {
	nodes := make(map[string]store.Node)
	if c.embedded {
		nodes[LocalNodeID] = store.Node{ID: LocalNodeID, CertGeneration: localGeneration}
	}
	provisioned, err := c.store.ListNodes(ctx)
	if err != nil {
		c.log.Error("list nodes for publishing", zap.Error(err))
		return nodes
	}
	for _, node := range provisioned {
		nodes[node.ID] = node
	}
	return nodes
}

// validateNodeReferences checks that a site is placed only on nodes that exist and that its paths pass only
// through nodes and proxies that exist.
func (c *Control) validateNodeReferences(ctx context.Context, site store.Site) error {
	if err := c.validatePlacementReferences(ctx, site); err != nil {
		return err
	}
	return c.validatePathReferences(ctx, site)
}

func (c *Control) validatePlacementReferences(ctx context.Context, site store.Site) error {
	for _, id := range siteNodes(site.Config) {
		if id == LocalNodeID {
			if !c.embedded {
				return fmt.Errorf("this controller runs no embedded node; place the site on registered nodes")
			}
			continue
		}
		if _, err := c.store.GetNode(ctx, id); err != nil {
			if errors.Is(err, store.ErrNodeNotFound) {
				return fmt.Errorf("node %q does not exist", id)
			}
			return err
		}
	}
	return nil
}

type nodeView struct {
	store.Node
	Embedded          bool                 `json:"embedded,omitempty"`
	Registered        bool                 `json:"registered"`
	Online            bool                 `json:"online"`
	LastSeen          string               `json:"lastSeen,omitempty"`
	Version           string               `json:"version,omitempty"`
	AppliedRevision   int64                `json:"appliedRevision"`
	PublishedRevision int64                `json:"publishedRevision"`
	InSync            bool                 `json:"inSync"`
	Errors            map[string]string    `json:"errors,omitempty"`
	Running           []string             `json:"running"`
	RelayError        string               `json:"relayError,omitempty"`
	Links             []overlay.LinkStatus `json:"links"`
	// Logs summarizes the node's log spool and upload pipeline health (D23/D24/D25); nil until the node reports
	// a status carrying it, and always nil on the embedded node (see southbound.LogStats's doc comment).
	Logs *southbound.LogStats `json:"logs,omitempty"`
}

func (c *Control) nodeView(node store.Node) nodeView {
	view := nodeView{Node: node, Registered: node.CertGeneration > 0, Running: []string{}, Links: []overlay.LinkStatus{}}
	if published, ok := c.published.Snapshot(node.ID); ok {
		view.PublishedRevision = published.Revision
	}
	if runtime, ok := c.nodes.snapshot(node.ID); ok {
		view.Online = runtime.streams > 0
		if !runtime.lastSeen.IsZero() {
			view.LastSeen = runtime.lastSeen.UTC().Format(time.RFC3339)
		}
		view.Version, view.AppliedRevision, view.Errors = runtime.status.Version, runtime.status.Revision, runtime.status.Errors
		view.RelayError = runtime.status.RelayError
		if runtime.status.Running != nil {
			view.Running = runtime.status.Running
		}
		if runtime.status.Links != nil {
			view.Links = runtime.status.Links
		}
		view.Logs = runtime.status.Logs
	}
	view.InSync = view.Online && view.AppliedRevision == view.PublishedRevision
	return view
}

func (c *Control) localNodeView() nodeView {
	c.opMu.Lock()
	revision, errs := c.localRevision, c.localErrors
	links := []overlay.LinkStatus{}
	if c.overlay != nil {
		links = c.overlay.Links()
	}
	c.opMu.Unlock()
	view := nodeView{Node: store.Node{ID: LocalNodeID, Name: "Embedded node"}, Embedded: true, Registered: true, Online: true,
		AppliedRevision: revision, Running: c.engine.RunningSites(), Errors: errs, Links: links}
	if published, ok := c.published.Snapshot(LocalNodeID); ok {
		view.PublishedRevision = published.Revision
	}
	view.InSync = view.AppliedRevision == view.PublishedRevision
	return view
}

type nodeMutation struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	RelayAddress string `json:"relayAddress"`
}

func (m nodeMutation) validate() error {
	if !nodeIDPattern.MatchString(m.ID) || m.ID == LocalNodeID {
		return fmt.Errorf("node id must be a lowercase DNS label other than %q", LocalNodeID)
	}
	if strings.TrimSpace(m.Name) == "" || len(m.Name) > maxNodeNameBytes {
		return fmt.Errorf("node name must contain 1-%d bytes", maxNodeNameBytes)
	}
	if m.RelayAddress != "" {
		host, port, err := net.SplitHostPort(m.RelayAddress)
		if err != nil || host == "" {
			return fmt.Errorf("relayAddress must be host:port")
		}
		if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 {
			return fmt.Errorf("relayAddress port must be 1-65535")
		}
	}
	return nil
}

type joinTokenResponse struct {
	Node      nodeView `json:"node"`
	JoinToken string   `json:"joinToken"`
	ExpiresAt string   `json:"expiresAt"`
}

// issueJoinToken stores a fresh single-use token on node and returns it; earlier tokens stop working.
func (c *Control) issueJoinToken(ctx context.Context, node *store.Node) (string, error) {
	ca, err := c.ensureCA(ctx)
	if err != nil {
		return "", err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(secret)
	node.TokenHash = hashToken(encoded)
	node.TokenExpiresAt = time.Now().Add(joinTokenTTL).UTC().Format(time.RFC3339)
	return pki.JoinToken{NodeID: node.ID, Secret: encoded, CAFingerprint: ca.Fingerprint()}.String(), nil
}

func hashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func tokenMatches(node store.Node, secret string) bool {
	if node.TokenHash == "" {
		return false
	}
	expires, err := time.Parse(time.RFC3339, node.TokenExpiresAt)
	if err != nil || time.Now().After(expires) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hashToken(secret)), []byte(node.TokenHash)) == 1
}

func (c *Control) nodesAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		nodes, err := c.store.ListNodes(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		views := make([]nodeView, 0, len(nodes)+1)
		if c.embeddedNode() {
			views = append(views, c.localNodeView())
		}
		for _, node := range nodes {
			views = append(views, c.nodeView(node))
		}
		writeJSON(w, http.StatusOK, views)
	case http.MethodPost:
		var input nodeMutation
		if !decode(w, r, &input) {
			return
		}
		input.Name = strings.TrimSpace(input.Name)
		if err := input.validate(); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{err.Error()})
			return
		}
		c.opMu.Lock()
		defer c.opMu.Unlock()
		if _, err := c.store.GetNode(r.Context(), input.ID); err == nil {
			writeJSON(w, http.StatusConflict, apiError{fmt.Sprintf("node %q already exists", input.ID)})
			return
		} else if !errors.Is(err, store.ErrNodeNotFound) {
			writeError(w, err)
			return
		}
		node := store.Node{ID: input.ID, Name: input.Name, RelayAddress: input.RelayAddress}
		token, err := c.issueJoinToken(r.Context(), &node)
		if err != nil {
			writeError(w, err)
			return
		}
		if err := c.store.SaveNode(r.Context(), node); err != nil {
			writeError(w, err)
			return
		}
		c.publishLocked(r.Context(), publishScope{})
		writeJSON(w, http.StatusCreated, joinTokenResponse{Node: c.nodeView(node), JoinToken: token, ExpiresAt: node.TokenExpiresAt})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
	}
}

func (c *Control) nodeAPI(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/nodes/"), "/"), "/")
	id := parts[0]
	c.opMu.Lock()
	defer c.opMu.Unlock()
	node, err := c.store.GetNode(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	switch {
	case len(parts) == 2 && parts[1] == "token" && r.Method == http.MethodPost:
		token, err := c.issueJoinToken(r.Context(), &node)
		if err != nil {
			writeError(w, err)
			return
		}
		if err := c.store.SaveNode(r.Context(), node); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, joinTokenResponse{Node: c.nodeView(node), JoinToken: token, ExpiresAt: node.TokenExpiresAt})
	case len(parts) == 1 && r.Method == http.MethodPut:
		var input nodeMutation
		if !decode(w, r, &input) {
			return
		}
		input.ID, input.Name = id, strings.TrimSpace(input.Name)
		if err := input.validate(); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{err.Error()})
			return
		}
		if input.RelayAddress == "" && node.RelayAddress != "" {
			if users, err := c.sitesRelayingThrough(r.Context(), id); err != nil {
				writeError(w, err)
				return
			} else if len(users) > 0 {
				writeJSON(w, http.StatusConflict, apiError{fmt.Sprintf("node %q relays for sites %s and needs a relay address", id, strings.Join(users, ", "))})
				return
			}
		}
		node.Name, node.RelayAddress = input.Name, input.RelayAddress
		if err := c.store.SaveNode(r.Context(), node); err != nil {
			writeError(w, err)
			return
		}
		c.publishLocked(r.Context(), publishScope{})
		writeJSON(w, http.StatusOK, c.nodeView(node))
	case len(parts) == 1 && r.Method == http.MethodDelete:
		if users, err := c.sitesUsingNode(r.Context(), id); err != nil {
			writeError(w, err)
			return
		} else if len(users) > 0 {
			writeJSON(w, http.StatusConflict, apiError{fmt.Sprintf("node %q is used by sites %s", id, strings.Join(users, ", "))})
			return
		}
		if err := c.store.DeleteNode(r.Context(), id); err != nil {
			writeError(w, err)
			return
		}
		c.nodes.forget(id)
		c.logIngestLocks.forget(id)
		c.logIngestRate.forget(id)
		c.publishLocked(r.Context(), publishScope{})
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
	}
}

// sitesUsingNode lists sites placed on a node or relaying through it, so it cannot be deleted from under them.
func (c *Control) sitesUsingNode(ctx context.Context, id string) ([]string, error) {
	sites, err := c.store.List(ctx)
	if err != nil {
		return nil, err
	}
	var users []string
	for _, site := range sites {
		if slices.Contains(site.Config.Nodes, id) || relaysThrough(site, id) {
			users = append(users, site.ID)
		}
	}
	return users, nil
}

// sitesRelayingThrough lists sites whose paths pass through a node, which then needs a relay address.
func (c *Control) sitesRelayingThrough(ctx context.Context, id string) ([]string, error) {
	sites, err := c.store.List(ctx)
	if err != nil {
		return nil, err
	}
	var users []string
	for _, site := range sites {
		if relaysThrough(site, id) {
			users = append(users, site.ID)
		}
	}
	return users, nil
}

func relaysThrough(site store.Site, id string) bool {
	return slices.ContainsFunc(site.Config.Upstreams, func(u store.Upstream) bool {
		return slices.ContainsFunc(upstreamPaths(u), func(p store.UpstreamPath) bool {
			return slices.ContainsFunc(p.Via, func(h store.Hop) bool { return h.Node == id })
		})
	})
}

func (c *Control) embeddedNode() bool {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	return c.embedded
}

// siteMetrics adds up a site's counters across the nodes it runs on.
type siteMetricsView struct {
	dataplane.MetricsSnapshot
	ByNode map[string]dataplane.MetricsSnapshot `json:"byNode"`
}

func (c *Control) siteMetrics(site store.Site) siteMetricsView {
	view := siteMetricsView{ByNode: make(map[string]dataplane.MetricsSnapshot)}
	for _, nodeID := range siteNodes(site.Config) {
		var metrics dataplane.MetricsSnapshot
		if nodeID == LocalNodeID {
			metrics = c.engine.Metrics(site.ID)
		} else if runtime, ok := c.nodes.snapshot(nodeID); ok {
			metrics = runtime.status.Metrics[site.ID]
		}
		view.ByNode[nodeID] = metrics
		view.MetricsSnapshot = dataplane.MergeMetrics(view.MetricsSnapshot, metrics)
	}
	return view
}

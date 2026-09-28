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
	// protocolVersion/protocolStatus are the rpop protocol version the node reported on its most recent
	// authenticated southbound call and whether that is "current" or "outdated" relative to
	// southbound.ProtocolVersion (D27); both are zero/empty before the node's first such call.
	protocolVersion int
	protocolStatus  string
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
// can log when the node's drop counters increase (D25) without a separate, racy read-then-write, and whether
// sanitizeStatus dropped an out-of-range clock offset the node reported (D28), so the caller can log that too —
// the same rate-limiting-free, log-once-per-call discipline warnOnLogStatsRegressions' caller already applies.
// status is sanitized first (see sanitizeStatus): a node is a half-trusted party, and its link/path health arrives
// as free-form JSON the controller keeps in memory and echoes back over /api/nodes and /api/topology.
func (r *nodeRegistry) report(id string, status southbound.Status) (previousLogs *southbound.LogStats, clockOffsetDropped bool) {
	reportedOffset := status.ClockOffsetMillis
	status = sanitizeStatus(status)
	clockOffsetDropped = reportedOffset != nil && status.ClockOffsetMillis == nil
	r.mu.Lock()
	defer r.mu.Unlock()
	runtime := r.get(id)
	previousLogs = runtime.status.Logs
	runtime.status, runtime.lastSeen = status, time.Now()
	return previousLogs, clockOffsetDropped
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

// reportProtocolVersion records the protocol version a node reported on an authenticated southbound call
// (D27), and whether classifyProtocolVersion (internal/control/southbound.go) judged it outdated, and reports
// whether this call is the one that first noticed the node's status changed, so checkProtocolVersion can log a
// Warn once per transition rather than once per call — the same rate-limiting discipline as
// warnOnLogStatsRegressions.
func (r *nodeRegistry) reportProtocolVersion(id string, version int, outdated bool) (changed bool) {
	status := "current"
	if outdated {
		status = "outdated"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	runtime := r.get(id)
	changed = runtime.protocolStatus != status
	runtime.protocolVersion, runtime.protocolStatus = version, status
	return changed
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
	Embedded   bool `json:"embedded,omitempty"`
	Registered bool `json:"registered"`
	// CertGeneration mirrors store.Node.CertGeneration, which is tagged `json:"-"` so that nodeMutation (the PUT
	// body) can never carry it back in and clobber it: nodeMutation only has Name/RelayAddress fields, but a
	// naive `json:"certGeneration"` on the store type would still round-trip through any future code path that
	// decodes a store.Node directly. Exposing a read-only copy here keeps that guarantee while still letting the
	// console show it (see nodesPage's cert generation column).
	CertGeneration    int64                `json:"certGeneration"`
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
	// Paths summarizes the node's per-upstream path failover health (D18/D19/D20); empty until the node reports
	// a status carrying it.
	Paths []dataplane.UpstreamPathHealth `json:"paths,omitempty"`
	// Logs summarizes the node's log spool and upload pipeline health (D23/D24/D25); nil until the node reports
	// a status carrying it, and always nil on the embedded node (see southbound.LogStats's doc comment).
	Logs *southbound.LogStats `json:"logs,omitempty"`
	// ProtocolVersion/ProtocolStatus report the rpop wire protocol version the node last spoke on an
	// authenticated southbound call and whether that is "current" or "outdated" relative to this controller's
	// southbound.ProtocolVersion (D27); both are zero/empty until the node's first such call. The embedded node
	// (see localNodeView) always reports the controller's own version, since it runs in the same process.
	ProtocolVersion int    `json:"protocolVersion,omitempty"`
	ProtocolStatus  string `json:"protocolStatus,omitempty"`
	// ClockSkewMillis is the node's most recently reported clock offset relative to the controller (D28,
	// milliseconds; positive means the node's clock is ahead); nil until the node's first status report carries
	// one, which never happens for a node stuck on a pre-D28 build or sanitizeStatus's own range check having
	// dropped an out-of-range value. ClockSkewStatus is "warn" once |ClockSkewMillis| exceeds
	// clockSkewWarnThresholdMillis (see SetClockSkewWarnThreshold), "ok" otherwise, and empty alongside a nil
	// ClockSkewMillis — the same empty-until-first-report convention ProtocolStatus uses. The embedded node's
	// skew is always exactly 0 (see localNodeView), since it runs in the same process and clock as the controller.
	ClockSkewMillis *int64 `json:"clockSkewMillis,omitempty"`
	ClockSkewStatus string `json:"clockSkewStatus,omitempty"`
}

// DefaultClockSkewWarnThresholdMillis is nodeView/topologyAPI's default cutoff for ClockSkewStatus's "warn"
// value (D28, see Control.SetClockSkewWarnThreshold): the console does not treat every nonzero measured skew as
// noteworthy, only one large enough to plausibly matter for reading a trace timeline.
const DefaultClockSkewWarnThresholdMillis = 2000

// MinClockSkewWarnThresholdMillis and MaxClockSkewWarnThresholdMillis bound the value
// ValidateClockSkewWarnThreshold accepts (D28's -clock-skew-warn-threshold), mirroring how
// ValidateSouthboundMaxStreamsPerConn bounds its own D31 CLI knob: below the floor a normal NTP-style estimate
// over a real network would trip "warn" on nearly every heartbeat, and above the ceiling the setting stops being
// a meaningful trace-timeline caveat at all.
const (
	MinClockSkewWarnThresholdMillis = 100
	MaxClockSkewWarnThresholdMillis = 60 * 60 * 1000 // 1h
)

// ValidateClockSkewWarnThreshold rejects a value outside [MinClockSkewWarnThresholdMillis,
// MaxClockSkewWarnThresholdMillis] (D28), so a bad -clock-skew-warn-threshold flag or RPOP_CLOCK_SKEW_WARN_THRESHOLD
// environment variable fails at startup with a clear message instead of silently misconfiguring the console's
// clock skew warning.
func ValidateClockSkewWarnThreshold(ms int64) error {
	if ms < MinClockSkewWarnThresholdMillis || ms > MaxClockSkewWarnThresholdMillis {
		return fmt.Errorf("clock skew warn threshold must be between %d and %d milliseconds, got %d", MinClockSkewWarnThresholdMillis, MaxClockSkewWarnThresholdMillis, ms)
	}
	return nil
}

// clockSkewZero returns a fresh pointer to 0: the embedded node's constant clock skew (D28), since it runs in the
// same process and clock as the controller and has nothing to measure. A fresh allocation each call rather than
// one shared package-level pointer, consistent with every other field of nodeView/topologyNode being its own copy
// per call, even though nothing today would mutate through a shared one.
func clockSkewZero() *int64 {
	zero := int64(0)
	return &zero
}

// clockSkewView turns a raw reported offset (nil if the node never reported one, or sanitizeStatus dropped an
// out-of-range value) into nodeView/topologyNode's pair of exposed fields (D28). Deliberately lock-free (see
// clockSkewWarnThresholdMillis's doc comment): nodeAPI already holds c.opMu for its whole handler around a single
// node, and nodeView/tunnelEventViews are its (and the list/topology/tunnel-event handlers') only path to this
// method, so taking opMu here would deadlock every one of those requests against itself.
func (c *Control) clockSkewView(offsetMillis *int64) (*int64, string) {
	if offsetMillis == nil {
		return nil, ""
	}
	threshold := c.clockSkewWarnThresholdMillis.Load()
	status := "ok"
	if *offsetMillis > threshold || *offsetMillis < -threshold {
		status = "warn"
	}
	return offsetMillis, status
}

// nodeClockOffsetMillis is clockSkewView's underlying lookup (D28), addressed by node ID alone rather than a
// store.Node, for a caller (tunnelEventViews) that only has an ID from already-reported data and no store.Node to
// hand nodeView itself.
func (c *Control) nodeClockOffsetMillis(nodeID string) *int64 {
	if nodeID == LocalNodeID {
		return clockSkewZero()
	}
	runtime, ok := c.nodes.snapshot(nodeID)
	if !ok {
		return nil
	}
	return runtime.status.ClockOffsetMillis
}

func (c *Control) nodeView(node store.Node) nodeView {
	view := nodeView{Node: node, Registered: node.CertGeneration > 0, CertGeneration: node.CertGeneration,
		Running: []string{}, Links: []overlay.LinkStatus{}}
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
		view.Paths = runtime.status.Paths
		view.Logs = runtime.status.Logs
		view.ProtocolVersion, view.ProtocolStatus = runtime.protocolVersion, runtime.protocolStatus
		view.ClockSkewMillis, view.ClockSkewStatus = c.clockSkewView(runtime.status.ClockOffsetMillis)
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
	view := nodeView{Node: store.Node{ID: LocalNodeID, Name: "Embedded node"}, Embedded: true, Registered: true,
		CertGeneration: localGeneration, Online: true,
		AppliedRevision: revision, Running: c.engine.RunningSites(), Errors: errs, Links: links, Paths: c.engine.PathHealth(),
		ProtocolVersion: southbound.ProtocolVersion, ProtocolStatus: "current",
		ClockSkewMillis: clockSkewZero(), ClockSkewStatus: "ok"}
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
	// The embedded node never lives in the store (see knownNodesLocked), so GET /api/nodes/local has to be
	// special-cased here rather than falling into the store.GetNode lookup below like every other id does.
	// embeddedNode/localNodeView each take c.opMu themselves, so this branch must return before the lock below.
	if len(parts) == 1 && r.Method == http.MethodGet && id == LocalNodeID {
		c.nodeAPILocal(w)
		return
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()
	node, err := c.store.GetNode(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, c.nodeView(node))
	case len(parts) == 2 && parts[1] == "token" && r.Method == http.MethodPost:
		c.nodeAPIToken(w, r, node)
	case len(parts) == 1 && r.Method == http.MethodPut:
		c.nodeAPIUpdate(w, r, id, node)
	case len(parts) == 1 && r.Method == http.MethodDelete:
		c.nodeAPIDelete(w, r, id)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
	}
}

// nodeAPILocal handles GET /api/nodes/local: the embedded node's view, or 404 when this controller runs none.
func (c *Control) nodeAPILocal(w http.ResponseWriter) {
	if !c.embeddedNode() {
		writeError(w, store.ErrNodeNotFound)
		return
	}
	writeJSON(w, http.StatusOK, c.localNodeView())
}

// nodeBootstrapInfo answers GET /api/nodes/bootstrap-info: everything the console's node onboarding guide needs
// to render a working "rpop -mode node ..." command and deployment recipes without the operator having to know
// the controller's own configuration.
type nodeBootstrapInfo struct {
	Version string `json:"version"`
	Mode    string `json:"mode"`
	// SouthboundEnabled mirrors whether cmd/rpop actually started the southbound listener nodes register
	// against; SouthboundAddr/SouthboundPort are only meaningful when this is true.
	SouthboundEnabled bool   `json:"southboundEnabled"`
	SouthboundAddr    string `json:"southboundAddr,omitempty"`
	SouthboundPort    int    `json:"southboundPort,omitempty"`
	// ConsoleAddr/ConsoleHostnames mirror cmd/rpop's own -addr/-console-hostnames verbatim (see
	// SetBootstrapInfo), so the console's site editor can tell, without a second endpoint, whether a site's own
	// listen address would reuse the console's shared port and, if so, which hostnames are already claimed (see
	// docs/architecture/control-data-plane.md §5, "共享端口(第三段)").
	ConsoleAddr      string   `json:"consoleAddr"`
	ConsoleHostnames []string `json:"consoleHostnames,omitempty"`
	// NodeControllerURL and NodeImage mirror the matching /api/settings fields verbatim (including "" when
	// unset); the console derives its own defaults (a URL guessed from the browser's hostname, an image name
	// built from Version) rather than this endpoint baking them in.
	NodeControllerURL string `json:"nodeControllerUrl"`
	NodeImage         string `json:"nodeImage"`
}

// nodeBootstrapInfoAPI handles GET /api/nodes/bootstrap-info. Read-only and requires a session like every other
// route (see Handler/authMiddleware); it carries no secrets, but the controller's version, mode, and southbound
// listen address are still only ever handed to an authenticated console.
func (c *Control) nodeBootstrapInfoAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
		return
	}
	c.opMu.Lock()
	version, mode, southboundAddr := c.bootstrapVersion, c.bootstrapMode, c.bootstrapSouthboundAddr
	consoleAddr, consoleHostnames := c.bootstrapConsoleAddr, c.bootstrapConsoleHostnames
	c.opMu.Unlock()
	c.systemSettingsMu.RLock()
	nodeControllerURL, nodeImage := c.systemSettings.NodeControllerURL, c.systemSettings.NodeImage
	c.systemSettingsMu.RUnlock()
	info := nodeBootstrapInfo{
		Version: version, Mode: mode,
		ConsoleAddr: consoleAddr, ConsoleHostnames: consoleHostnames,
		NodeControllerURL: nodeControllerURL, NodeImage: nodeImage,
	}
	if southboundAddr != "" {
		info.SouthboundEnabled = true
		info.SouthboundAddr = southboundAddr
		if _, port, err := net.SplitHostPort(southboundAddr); err == nil {
			if number, err := strconv.Atoi(port); err == nil {
				info.SouthboundPort = number
			}
		}
	}
	writeJSON(w, http.StatusOK, info)
}

// nodeAPIToken handles POST /api/nodes/{id}/token: issue a fresh join token, invalidating any earlier one.
// Called with c.opMu already held (see nodeAPI).
func (c *Control) nodeAPIToken(w http.ResponseWriter, r *http.Request, node store.Node) {
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
}

// nodeAPIUpdate handles PUT /api/nodes/{id}: rename the node or change its relay address, republishing every
// node's snapshot when it did. Called with c.opMu already held (see nodeAPI).
func (c *Control) nodeAPIUpdate(w http.ResponseWriter, r *http.Request, id string, node store.Node) {
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
}

// nodeAPIDelete handles DELETE /api/nodes/{id}: refused while any site still depends on the node, otherwise
// forgets it everywhere and republishes. Called with c.opMu already held (see nodeAPI).
func (c *Control) nodeAPIDelete(w http.ResponseWriter, r *http.Request, id string) {
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
	// logIngestLocks needs no forget: its entries are reference-counted and self-remove once unused (see
	// keyedMutex's doc comment) rather than requiring one racing this deletion.
	c.logIngestRate.forget(id)
	if c.overlay != nil {
		// Immediate cleanup of D27's per-peer warn-once bookkeeping (overlay.Overlay.ForgetPeer), rather than
		// waiting for the embedded node's overlay to notice id missing from its next Apply (it eventually would,
		// see forgetStalePeers) — stage 7 review, LOW item 4.
		c.overlay.ForgetPeer(id)
	}
	c.publishLocked(r.Context(), publishScope{})
	w.WriteHeader(http.StatusNoContent)
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

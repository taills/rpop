// Package agent runs a data-plane node: it registers with the controller, follows the snapshots the controller
// streams to it, applies them to a local engine, and reports status. It keeps serving the last snapshot it
// applied while the controller is unreachable, including across restarts.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/dataplane"
	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/snapshot"
	"github.com/rpop-project/rpop/internal/southbound"
	"github.com/rpop-project/rpop/internal/spool"
)

const (
	snapshotFile      = "snapshot.json"
	statusInterval    = 15 * time.Second
	requestTimeout    = 15 * time.Second
	minBackoff        = time.Second
	maxBackoff        = 30 * time.Second
	authRetryInterval = time.Minute
	// watchSilenceLimit is how long a watch stream may carry nothing before the node reconnects.
	watchSilenceLimit = 3 * southbound.PingInterval
)

// Config configures a node.
type Config struct {
	// ControllerURL is the base URL of the controller's southbound listener, e.g. https://controller:7443.
	ControllerURL string
	// JoinToken registers the node when DataDir holds no identity yet, or after the controller revoked it.
	JoinToken string
	// DataDir holds the node key, certificates, and the last applied snapshot.
	DataDir string
	// RelayListen overrides the address the relay port binds, for a node whose relay address other nodes dial
	// is forwarded to a different local port. Empty binds the port of the relay address on every interface.
	RelayListen string
	Version     string
	// LogDir holds the node's local log spool (<LogDir>/spool), which buffers access log records and tunnel
	// events for upload to the controller (D23). Empty falls back to a "logs" directory under DataDir, so tests
	// and other callers that only set DataDir still get a working spool location.
	LogDir string
	// LogSpoolQuotaBytes bounds the spool's disk usage; 0 uses spool.DefaultQuotaBytes (D25).
	LogSpoolQuotaBytes int64
	// LogUploadRateBytesPerSecond throttles how fast spooled segments are uploaded; 0 uses
	// spool.DefaultUploadRateBytesPerSecond (D25).
	LogUploadRateBytesPerSecond int64
}

// session is the identity the node authenticates with and the client that presents it.
type session struct {
	identity *pki.Identity
	client   *http.Client
}

// Agent is a running data-plane node.
type Agent struct {
	cfg     Config
	base    *url.URL
	log     *zap.Logger
	engine  *dataplane.Engine
	started time.Time
	current atomic.Pointer[session]
	kick    chan struct{}
	// spool and uploader are created once at the start of Run, before any goroutine that might read them
	// starts, and never replaced afterward (unlike overlay, which re-registration does replace); reading them
	// without a lock is therefore safe.
	spool    *spool.Spool
	uploader *spool.Uploader

	mu       sync.Mutex
	revision int64
	errors   map[string]string
	// overlay carries upstream paths across nodes under the node's current identity; relayError explains a relay
	// port that could not bind. Both are guarded by mu.
	overlay    *overlay.Overlay
	relayError string
}

// agentPaths dials upstream paths through the overlay of the node's current identity, which is replaced when
// the node registers again.
type agentPaths struct{ a *Agent }

func (p agentPaths) DialPath(ctx context.Context, path snapshot.Path) (net.Conn, error) {
	p.a.mu.Lock()
	o := p.a.overlay
	p.a.mu.Unlock()
	if o == nil {
		return nil, errors.New("the node has no identity to reach other nodes with")
	}
	return o.DialPath(ctx, path)
}

// New validates the configuration of a node.
func New(cfg Config, log *zap.Logger) (*Agent, error) {
	base, err := url.Parse(cfg.ControllerURL)
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, fmt.Errorf("controller URL must be https://host:port, got %q", cfg.ControllerURL)
	}
	if cfg.DataDir == "" {
		return nil, errors.New("node data directory is required")
	}
	a := &Agent{cfg: cfg, base: base, log: log, engine: dataplane.New(log), started: time.Now(), kick: make(chan struct{}, 1)}
	a.engine.SetPathDialer(agentPaths{a})
	return a, nil
}

// Engine is the data plane the node drives.
func (a *Agent) Engine() *dataplane.Engine { return a.engine }

// NodeID is the ID in the node's certificate, empty before the node has an identity.
func (a *Agent) NodeID() string {
	if s := a.current.Load(); s != nil {
		return s.identity.NodeID
	}
	return ""
}

// Run brings the node up and follows the controller until ctx ends, then stops every site.
func (a *Agent) Run(ctx context.Context) error {
	if err := os.MkdirAll(a.cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create node data directory: %w", err)
	}
	logDir := a.cfg.LogDir
	if logDir == "" {
		logDir = filepath.Join(a.cfg.DataDir, "logs")
	}
	sp, err := spool.NewSpool(spool.Config{
		Dir: filepath.Join(logDir, "spool"), QuotaBytes: a.cfg.LogSpoolQuotaBytes, Log: a.log.Named("spool"),
	})
	if err != nil {
		return fmt.Errorf("open log spool: %w", err)
	}
	// Stop serving sites and close every overlay link, in that order, before closing the spool: closing a site's
	// pooled connections (engine.StopAll, via siteRuntime.release) and tearing down links (closeOverlay) can each
	// still produce a final access log record or a tunnel "ended" event (D22). A record or event handed to the
	// spool after its write goroutine has already stopped is only structurally accepted, never drained or
	// uploaded (see Spool.Close's doc comment) - closing the spool last is what keeps that shutdown-time tail of
	// records from being silently lost instead of counted as a drop.
	defer func() {
		a.engine.StopAll()
		a.closeOverlay()
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := sp.Close(closeCtx); err != nil {
			a.log.Warn("close log spool", zap.Error(err))
		}
	}()
	// a.spool must be set before the first call to use() below, so it (and the overlay it wires up) is in place
	// by the time any site starts producing tunnel events; see use()'s doc comment.
	a.spool = sp
	a.engine.SetAccessLogWriter(sp)
	a.uploader = spool.NewUploader(sp, spool.UploaderConfig{
		Endpoint:           a.endpoint(southbound.LogsPath),
		Client:             func() *http.Client { return a.current.Load().client },
		RateBytesPerSecond: a.cfg.LogUploadRateBytesPerSecond,
		Log:                a.log.Named("log-upload"),
	})

	identity, err := loadIdentity(a.cfg.DataDir)
	switch {
	case err == nil:
		a.use(identity)
		a.restore()
	case errors.Is(err, os.ErrNotExist):
		if a.cfg.JoinToken == "" {
			return fmt.Errorf("no node identity in %s; start the node once with a join token", a.cfg.DataDir)
		}
		if err := a.registerUntilDone(ctx); err != nil {
			return err
		}
		a.restore()
	default:
		return err
	}
	a.log.Info("node started", zap.String("node", a.NodeID()), zap.String("controller", a.base.String()))

	var wg sync.WaitGroup
	wg.Go(func() { a.statusLoop(ctx) })
	wg.Go(func() { a.renewLoop(ctx) })
	wg.Go(func() { a.uploader.Run(ctx) })
	a.watchLoop(ctx)
	wg.Wait()
	return nil
}

// use installs identity as the node's current one and gives it a fresh overlay (links and the relay port
// authenticate with the node certificate, so a new registration needs a new overlay; the next snapshot
// configures it, and the old one must release the relay port first). The overlay is wired to the node's log
// spool here too, since a re-registration (see watchLoop) replaces the overlay instance without going through
// Run again.
//
// The new overlay is built and the pointer swapped under a.mu, but the old overlay's Close runs after
// releasing it: Close waits for every link's maintain goroutine to exit and can, for one slow or unreachable
// peer, still take a little while even with link.retire's own dial cancellation. Holding a.mu for that would
// stall every other caller that needs it — agentPaths.DialPath, applyOverlay, status — for as long as Close
// took, on every re-registration.
func (a *Agent) use(identity *pki.Identity) {
	previous := a.current.Swap(&session{identity: identity, client: newClient(identity.ControllerClientConfig())})
	if previous != nil {
		previous.client.CloseIdleConnections()
	}
	next := overlay.New(identity, a.log.Named("overlay"))
	if a.spool != nil {
		next.SetTunnelEventSink(a.spool)
	}
	a.mu.Lock()
	previousOverlay := a.overlay
	a.overlay = next
	a.mu.Unlock()
	if previousOverlay != nil {
		previousOverlay.Close()
	}
}

func (a *Agent) closeOverlay() {
	a.mu.Lock()
	o := a.overlay
	a.overlay = nil
	a.mu.Unlock()
	if o != nil {
		o.Close()
	}
}

// registerUntilDone registers with the join token, retrying while the controller is unreachable.
func (a *Agent) registerUntilDone(ctx context.Context) error {
	backoff := minBackoff
	for {
		identity, err := register(ctx, a.base, a.cfg.DataDir, a.cfg.JoinToken)
		if err == nil {
			a.use(identity)
			a.log.Info("node registered with the controller", zap.String("node", identity.NodeID))
			return nil
		}
		if isUnauthorized(err) {
			return fmt.Errorf("the controller rejected the join token; create a new one: %w", err)
		}
		a.log.Warn("node registration failed; retrying", zap.Error(err), zap.Duration("retry_in", backoff))
		if !sleep(ctx, jitter(backoff)) {
			return ctx.Err()
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// watchLoop keeps a watch stream open, reconnecting with backoff.
func (a *Agent) watchLoop(ctx context.Context) {
	backoff := minBackoff
	for ctx.Err() == nil {
		opened := time.Now()
		err := a.watch(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(opened) > time.Minute {
			backoff = minBackoff
		}
		wait := jitter(backoff)
		if isUnauthorized(err) {
			wait = authRetryInterval
			if a.cfg.JoinToken != "" {
				identity, regErr := register(ctx, a.base, a.cfg.DataDir, a.cfg.JoinToken)
				if regErr == nil {
					a.use(identity)
					a.log.Info("node registered again after the controller revoked its certificate")
					continue
				}
				err = fmt.Errorf("%w; registering again failed: %v", err, regErr)
			}
			a.log.Error("the controller no longer accepts this node's certificate; start the node with a new join token", zap.Error(err))
		} else {
			a.log.Warn("controller watch ended; reconnecting", zap.Error(err), zap.Duration("retry_in", wait))
		}
		if !sleep(ctx, wait) {
			return
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// watch follows one watch stream until it fails or goes silent.
func (a *Agent) watch(ctx context.Context) error {
	s := a.current.Load()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint(southbound.WatchPath), nil)
	if err != nil {
		return err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err := responseError(response); err != nil {
		return err
	}
	a.log.Info("connected to the controller", zap.String("protocol", response.Proto))
	silence := time.AfterFunc(watchSilenceLimit, func() { cancel(errors.New("watch stream went silent")) })
	defer silence.Stop()
	decoder := json.NewDecoder(response.Body)
	for {
		var frame southbound.Frame
		if err := decoder.Decode(&frame); err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return cause
			}
			return fmt.Errorf("read watch stream: %w", err)
		}
		silence.Reset(watchSilenceLimit)
		if frame.Type == southbound.FrameSnapshot && frame.Snapshot != nil {
			a.apply(*frame.Snapshot)
		}
	}
}

// apply hands a snapshot to the overlay and then to the engine, so links toward the peers of new paths are
// already dialing when sites start using them. Unchanged sites are left alone, so re-applying after a reconnect
// only retries the sites that failed before.
func (a *Agent) apply(s snapshot.Snapshot) {
	if s.NodeID != a.NodeID() {
		a.log.Error("ignoring a snapshot rendered for another node", zap.String("snapshot_node", s.NodeID))
		return
	}
	a.applyOverlay(s)
	errs := a.engine.Apply(s.Sites)
	messages := make(map[string]string, len(errs))
	for id, err := range errs {
		messages[id] = err.Error()
		a.log.Warn("site did not apply; it keeps its previous configuration", zap.String("site", id), zap.Error(err))
	}
	a.mu.Lock()
	a.revision, a.errors = s.Revision, messages
	a.mu.Unlock()
	if err := a.saveCache(s, errs); err != nil {
		a.log.Warn("could not cache the applied snapshot", zap.Error(err))
	}
	a.kickStatus()
}

func (a *Agent) applyOverlay(s snapshot.Snapshot) {
	if s.RelayListen != "" && a.cfg.RelayListen != "" {
		s.RelayListen = a.cfg.RelayListen
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.relayError = ""
	if err := a.overlay.Apply(s); err != nil {
		a.relayError = err.Error()
		a.log.Error("the relay port did not start; tunnels through this node fail over", zap.Error(err))
	}
}

// saveCache stores what the node actually serves: a site that failed to apply is cached with the spec it still
// runs, or not at all.
func (a *Agent) saveCache(s snapshot.Snapshot, errs map[string]error) error {
	sites := make([]snapshot.Site, 0, len(s.Sites))
	for _, site := range s.Sites {
		if errs[site.ID] != nil {
			spec, running := a.engine.Spec(site.ID)
			if !running {
				continue
			}
			site = spec
		}
		sites = append(sites, site)
	}
	s.Sites = sites
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(a.cfg.DataDir, snapshotFile), data)
}

// restore serves the cached snapshot before the controller is reachable.
func (a *Agent) restore() {
	data, err := os.ReadFile(filepath.Join(a.cfg.DataDir, snapshotFile))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var s snapshot.Snapshot
	if err == nil {
		err = json.Unmarshal(data, &s)
	}
	if err != nil {
		a.log.Warn("ignoring an unreadable snapshot cache", zap.Error(err))
		return
	}
	if s.NodeID != a.NodeID() {
		a.log.Warn("ignoring a snapshot cache of another node", zap.String("snapshot_node", s.NodeID))
		return
	}
	a.log.Info("serving the cached snapshot until the controller answers", zap.Int64("revision", s.Revision), zap.Int("sites", len(s.Sites)))
	a.apply(s)
}

func (a *Agent) kickStatus() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

// statusLoop reports the node's state periodically and right after every snapshot.
func (a *Agent) statusLoop(ctx context.Context) {
	ticker := time.NewTicker(statusInterval)
	defer ticker.Stop()
	failing := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-a.kick:
		}
		err := a.sendStatus(ctx)
		switch {
		case err != nil && !failing && ctx.Err() == nil:
			a.log.Warn("could not report status to the controller", zap.Error(err))
		case err == nil && failing:
			a.log.Info("status reports reach the controller again")
		}
		failing = err != nil
	}
}

func (a *Agent) status() southbound.Status {
	a.mu.Lock()
	status := southbound.Status{Version: a.cfg.Version, Revision: a.revision, StartedAt: a.started, RelayError: a.relayError}
	if a.overlay != nil {
		status.Links = a.overlay.Links()
	}
	if len(a.errors) > 0 {
		status.Errors = make(map[string]string, len(a.errors))
		for id, message := range a.errors {
			status.Errors[id] = message
		}
	}
	a.mu.Unlock()
	status.Running = a.engine.RunningSites()
	status.Metrics = make(map[string]dataplane.MetricsSnapshot, len(status.Running))
	for _, id := range status.Running {
		status.Metrics[id] = a.engine.Metrics(id)
	}
	status.Paths = a.engine.PathHealth()
	if a.spool != nil {
		stats := a.spool.Stats()
		status.Logs = &southbound.LogStats{
			AccessLogQueueDropped:   stats.AccessLogQueueDropped,
			TunnelEventQueueDropped: stats.TunnelEventQueueDropped,
			QuotaDroppedSegments:    stats.QuotaDroppedSegments,
			QuotaDroppedBytes:       stats.QuotaDroppedBytes,
			PendingSegments:         stats.PendingSegments,
			PendingBytes:            stats.PendingBytes,
			AckedSegment:            stats.AckedSegment,
			LastUploadError:         a.uploader.LastError(),
		}
	}
	return status
}

func (a *Agent) sendStatus(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return postJSON(ctx, a.current.Load().client, a.endpoint(southbound.StatusPath), a.status(), nil)
}

func (a *Agent) endpoint(path string) string {
	return a.base.JoinPath(path).String()
}

func jitter(d time.Duration) time.Duration {
	return d/2 + rand.N(d/2+1)
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

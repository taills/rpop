package control

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/southbound"
	"github.com/rpop-project/rpop/internal/store"
)

const (
	maxRegisterFailures   = 10
	registerFailureWindow = 15 * time.Minute
	maxSouthboundBody     = 64 << 10
	maxStatusBody         = 4 << 20
	// frameWriteTimeout bounds one watch frame write, so a node that stopped reading cannot pin the stream.
	frameWriteTimeout = 30 * time.Second
	// MaxConcurrentSouthboundStreamsPerConn bounds how many concurrent HTTP/2 streams the southbound listener
	// accepts on one connection (stage 5 security review item 5), the same protection the relay port already
	// applies to node-to-node links (see internal/overlay's maxStreamsPerConn). A node normally needs only a
	// handful at once — one long-lived watch stream plus, occasionally, a register/renew/status/logs request —
	// so this is far looser than that per-connection reality requires, while still refusing an unbounded number
	// of streams from a single connection.
	MaxConcurrentSouthboundStreamsPerConn = 100
)

var errNodeUnauthenticated = errors.New("node certificate is missing, unknown, or revoked")

// failureLimiter counts failed attempts per client address in fixed windows.
type failureLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	attempts map[string]loginAttempt
}

func newFailureLimiter(limit int, window time.Duration) *failureLimiter {
	return &failureLimiter{limit: limit, window: window, attempts: make(map[string]loginAttempt)}
}

func (l *failureLimiter) allowed(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for other, attempt := range l.attempts {
		if now.Sub(attempt.windowStart) >= l.window {
			delete(l.attempts, other)
		}
	}
	return l.attempts[key].failures < l.limit
}

func (l *failureLimiter) fail(key string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	attempt := l.attempts[key]
	if attempt.windowStart.IsZero() || now.Sub(attempt.windowStart) >= l.window {
		attempt = loginAttempt{windowStart: now}
	}
	attempt.failures++
	l.attempts[key] = attempt
}

// SouthboundTLSConfig serves the southbound API with a controller certificate from the internal CA. Client
// certificates are verified when presented; registration presents none and authenticates with its join token.
func (c *Control) SouthboundTLSConfig(ctx context.Context) (*tls.Config, error) {
	ca, err := c.ensureCA(ctx)
	if err != nil {
		return nil, err
	}
	certificate, err := ca.IssueController()
	if err != nil {
		return nil, fmt.Errorf("issue controller certificate: %w", err)
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate},
		ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: ca.Pool(), NextProtos: []string{"h2", "http/1.1"},
	}, nil
}

// SouthboundHandler serves the API that data-plane nodes call.
func (c *Control) SouthboundHandler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST "+southbound.RegisterPath, c.southboundRegister)
	m.HandleFunc("POST "+southbound.RenewPath, c.southboundRenew)
	m.HandleFunc("GET "+southbound.WatchPath, c.southboundWatch)
	m.HandleFunc("POST "+southbound.StatusPath, c.southboundStatus)
	m.HandleFunc("POST "+southbound.LogsPath, c.southboundLogs)
	return m
}

// SouthboundHTTP2Config is the HTTP/2 tuning the southbound listener's *http.Server should use (stage 5
// security review item 5): the caller is responsible for setting it as that Server's HTTP2 field (mirroring
// how internal/overlay's relay port configures its own listener), since the *http.Server itself is constructed
// outside this package (cmd/rpop's startSouthbound). Only concurrency is bounded here — no per-stream or
// per-connection flow-control window, and no ping timeouts — so the long-lived watch stream (southboundWatch)
// is never affected by anything this returns; that stream's own frameWriteTimeout is what bounds a stalled
// reader.
func SouthboundHTTP2Config() *http.HTTP2Config {
	return &http.HTTP2Config{MaxConcurrentStreams: MaxConcurrentSouthboundStreamsPerConn}
}

// authenticateNode identifies the node behind a request by its client certificate. The TLS handshake already
// verified the chain; the node must still exist and the certificate must be of its current registration.
func (c *Control) authenticateNode(r *http.Request) (store.Node, error) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return store.Node{}, errNodeUnauthenticated
	}
	certificate := r.TLS.PeerCertificates[0]
	id, ok := pki.NodeIDFromCertificate(certificate)
	if !ok {
		return store.Node{}, errNodeUnauthenticated
	}
	node, err := c.store.GetNode(r.Context(), id)
	if errors.Is(err, store.ErrNodeNotFound) {
		return store.Node{}, errNodeUnauthenticated
	}
	if err != nil {
		return store.Node{}, err
	}
	if generation, ok := pki.NodeGeneration(certificate); !ok || generation != node.CertGeneration {
		return store.Node{}, errNodeUnauthenticated
	}
	return node, nil
}

func writeNodeAuthError(w http.ResponseWriter, err error) {
	if errors.Is(err, errNodeUnauthenticated) {
		writeJSON(w, http.StatusUnauthorized, apiError{err.Error()})
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, apiError{"node authentication is unavailable"})
}

// decodeSouthbound reads a JSON body. Unknown fields are accepted so newer nodes can talk to older controllers.
func decodeSouthbound(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{"invalid request body"})
		return false
	}
	return true
}

func remoteHost(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func (c *Control) southboundRegister(w http.ResponseWriter, r *http.Request) {
	client := remoteHost(r)
	if !c.registrations.allowed(client) {
		writeJSON(w, http.StatusTooManyRequests, apiError{"too many failed registrations; try again later"})
		return
	}
	var request southbound.RegisterRequest
	if !decodeSouthbound(w, r, maxSouthboundBody, &request) {
		return
	}
	reject := func(reason string) {
		c.registrations.fail(client)
		c.log.Warn("node registration rejected", zap.String("remote", client), zap.String("reason", reason))
		writeJSON(w, http.StatusUnauthorized, apiError{"join token is invalid, used, or expired"})
	}
	token, err := pki.ParseJoinToken(request.Token)
	if err != nil {
		reject("malformed token")
		return
	}
	ca, err := c.ensureCA(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{"internal CA is unavailable"})
		return
	}
	if token.CAFingerprint != ca.Fingerprint() {
		reject("token pins another CA")
		return
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()
	node, err := c.store.GetNode(r.Context(), token.NodeID)
	if err != nil || !tokenMatches(node, token.Secret) {
		reject("unknown node or token mismatch")
		return
	}
	certificate, certificatePEM, err := ca.SignNode(request.CSRPEM, node.ID, node.CertGeneration+1)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{err.Error()})
		return
	}
	node.TokenHash, node.TokenExpiresAt = "", ""
	node.CertGeneration++
	node.CertNotAfter = certificate.NotAfter.UTC().Format(time.RFC3339)
	node.RegisteredAt = time.Now().UTC().Format(time.RFC3339)
	if err := c.store.SaveNode(r.Context(), node); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{"could not record the registration"})
		return
	}
	// A fresh registration invalidates the previous generation's certificate, so its spool can never upload
	// again under it; the node's own spool always starts a new one from segment 1 after losing its identity
	// (reinstall, lost data directory, ...), so replaying against the old high-water mark would silently drop
	// its first segment as an already-acked duplicate. Resetting it here is safe: nothing can still upload under
	// the retired generation, and a spool that keeps running under the new one only ever grows its segment
	// numbers, so this can at worst cause a rare double write (a segment already durably ingested, whose ack the
	// node never saw before re-registering), never data loss. Renewal (southboundRenew) keeps the generation and
	// must leave this alone. Taking the node's own southboundLogs lock first serializes the reset against an
	// upload already in flight for this node, the same way two uploads serialize against each other, so the two
	// can never interleave into a torn read-modify-write of the mark. See docs/architecture/control-data-plane.md
	// §5.
	unlockLogs := c.logIngestLocks.lock(node.ID)
	hwmErr := c.store.UpdateNodeLogHWM(r.Context(), node.ID, 0)
	unlockLogs()
	if hwmErr != nil {
		c.log.Warn("reset log high-water mark after registration", zap.String("node", node.ID), zap.Error(hwmErr))
	}
	// Peers must accept the new certificate and refuse older ones, so every node gets a snapshot naming the
	// current generation; streams opened with certificates from an earlier registration re-authenticate and end.
	c.publishLocked(r.Context(), publishScope{})
	c.log.Info("node registered", zap.String("node", node.ID), zap.String("remote", client))
	writeJSON(w, http.StatusOK, southbound.RegisterResponse{CertificatePEM: certificatePEM, CAPEM: ca.CertPEM})
}

func (c *Control) southboundRenew(w http.ResponseWriter, r *http.Request) {
	var request southbound.RenewRequest
	if !decodeSouthbound(w, r, maxSouthboundBody, &request) {
		return
	}
	ca, err := c.ensureCA(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{"internal CA is unavailable"})
		return
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()
	node, err := c.authenticateNode(r)
	if err != nil {
		writeNodeAuthError(w, err)
		return
	}
	certificate, certificatePEM, err := ca.SignNode(request.CSRPEM, node.ID, node.CertGeneration)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{err.Error()})
		return
	}
	node.CertNotAfter = certificate.NotAfter.UTC().Format(time.RFC3339)
	if err := c.store.SaveNode(r.Context(), node); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{"could not record the renewal"})
		return
	}
	c.log.Info("node certificate renewed", zap.String("node", node.ID), zap.Time("not_after", certificate.NotAfter))
	writeJSON(w, http.StatusOK, southbound.RenewResponse{CertificatePEM: certificatePEM})
}

// southboundWatch streams the node's snapshot whenever a new revision is published, with pings in between so
// both sides notice a dead connection.
func (c *Control) southboundWatch(w http.ResponseWriter, r *http.Request) {
	node, err := c.authenticateNode(r)
	if err != nil {
		writeNodeAuthError(w, err)
		return
	}
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if err := controller.Flush(); err != nil {
		return
	}
	c.nodes.connected(node.ID, 1)
	defer c.nodes.connected(node.ID, -1)
	encoder := json.NewEncoder(w)
	send := func(frame southbound.Frame) error {
		_ = controller.SetWriteDeadline(time.Now().Add(frameWriteTimeout))
		if err := encoder.Encode(frame); err != nil {
			return err
		}
		return controller.Flush()
	}
	ping := time.NewTicker(southbound.PingInterval)
	defer ping.Stop()
	sent := int64(-1)
	for {
		current, ok, changed := c.published.watch(node.ID)
		if ok && current.Revision != sent {
			if err := send(southbound.Frame{Type: southbound.FrameSnapshot, Snapshot: &current}); err != nil {
				c.log.Info("node watch stream ended", zap.String("node", node.ID), zap.Error(err))
				return
			}
			sent = current.Revision
		}
		select {
		case <-r.Context().Done():
			return
		case <-changed:
			if _, err := c.authenticateNode(r); err != nil {
				c.log.Info("closing watch stream of a node that is no longer authorized", zap.String("node", node.ID))
				return
			}
		case <-ping.C:
			if err := send(southbound.Frame{Type: southbound.FramePing}); err != nil {
				c.log.Info("node watch stream ended", zap.String("node", node.ID), zap.Error(err))
				return
			}
		}
	}
}

func (c *Control) southboundStatus(w http.ResponseWriter, r *http.Request) {
	node, err := c.authenticateNode(r)
	if err != nil {
		writeNodeAuthError(w, err)
		return
	}
	var status southbound.Status
	if !decodeSouthbound(w, r, maxStatusBody, &status) {
		return
	}
	previousLogs := c.nodes.report(node.ID, status)
	c.warnOnLogStatsRegressions(node.ID, previousLogs, status.Logs)
	w.WriteHeader(http.StatusNoContent)
}

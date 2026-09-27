package dataplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/snapshot"
)

const (
	minPathCooldown = time.Second
	maxPathCooldown = time.Minute
	// pathEstablishTimeout bounds connecting along one path, including every relay and the exit's dial to the
	// upstream; links are kept warm, so it only has to cover the tunnel and the final hop.
	pathEstablishTimeout = 10 * time.Second
)

// PathDialer opens connections along upstream paths.
type PathDialer interface {
	DialPath(ctx context.Context, path snapshot.Path) (net.Conn, error)
}

// localPaths dials paths that stay on this node; crossing nodes needs the node's overlay.
type localPaths struct{}

func (localPaths) DialPath(ctx context.Context, path snapshot.Path) (net.Conn, error) {
	if path.FirstNode != "" {
		return nil, errors.New("this node has no overlay to reach other nodes")
	}
	return overlay.DialChain(ctx, path.Egress, path.Target)
}

// SetPathDialer provides the overlay that upstream paths across nodes use. Call it before Apply.
func (e *Engine) SetPathDialer(dialer PathDialer) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.paths = dialer
}

func (e *Engine) pathDialer() PathDialer {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.paths == nil {
		return localPaths{}
	}
	return e.paths
}

// pathDialError marks a failure to establish a connection along a path: nothing reached the upstream, so the
// request may safely try the next path.
type pathDialError struct {
	label string
	err   error
}

func (e *pathDialError) Error() string { return fmt.Sprintf("path %s: %v", e.label, e.err) }
func (e *pathDialError) Unwrap() error { return e.err }

// pathTransport is one candidate path with its own connection pool. Separate pools mean that after failing
// back to a preferred path, new requests never reuse connections of the fallback path, while requests already
// on it finish undisturbed.
type pathTransport struct {
	label     string
	transport *http.Transport

	mu       sync.Mutex
	failures int
	until    time.Time
}

func (p *pathTransport) coolingDown(now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return now.Before(p.until)
}

func (p *pathTransport) failed(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures++
	p.until = now.Add(min(minPathCooldown<<min(p.failures-1, 10), maxPathCooldown))
}

func (p *pathTransport) succeeded() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures, p.until = 0, time.Time{}
}

// failoverTransport sends each request on the first path, in priority order, that is not cooling down after
// a connection failure. When connecting fails before any request byte was sent, it moves on to the next
// path; a request that fails later is never retried, because the upstream may have acted on it.
type failoverTransport struct {
	paths            []*pathTransport
	establishTimeout time.Duration
}

func newPathTransports(base *http.Transport, paths []snapshot.Path, dialer PathDialer) (*failoverTransport, []*http.Transport) {
	f := &failoverTransport{establishTimeout: pathEstablishTimeout}
	transports := make([]*http.Transport, 0, len(paths))
	for _, path := range paths {
		transport := base.Clone()
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			// Connections outlive the context they were dialed with, so bounding it only bounds the attempt.
			ctx, cancel := context.WithTimeout(ctx, f.establishTimeout)
			defer cancel()
			conn, err := dialer.DialPath(ctx, path)
			if err != nil {
				return nil, &pathDialError{label: path.Label, err: err}
			}
			return conn, nil
		}
		f.paths = append(f.paths, &pathTransport{label: path.Label, transport: transport})
		transports = append(transports, transport)
	}
	return f, transports
}

// order lists the paths to try: those ready in priority order, then those cooling down, so a request still
// goes out when every path recently failed.
func (f *failoverTransport) order(now time.Time) []*pathTransport {
	ordered := make([]*pathTransport, 0, len(f.paths))
	var cooling []*pathTransport
	for _, p := range f.paths {
		if p.coolingDown(now) {
			cooling = append(cooling, p)
		} else {
			ordered = append(ordered, p)
		}
	}
	return append(ordered, cooling...)
}

func (f *failoverTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	attempt, body := withReplayGuard(req)
	var lastErr error
	for _, p := range f.order(time.Now()) {
		response, err := p.transport.RoundTrip(attempt)
		if err == nil {
			p.succeeded()
			return response, nil
		}
		var dialErr *pathDialError
		if !errors.As(err, &dialErr) {
			return nil, err
		}
		p.failed(time.Now())
		lastErr = err
		if body != nil && body.read.Load() {
			break
		}
	}
	return nil, lastErr
}

// replayGuard lets a request body be offered to another path after a failed connection attempt: it records
// whether any byte was read and keeps the transport from closing the body it hands back.
type replayGuard struct {
	io.ReadCloser
	read atomic.Bool
}

func (g *replayGuard) Read(p []byte) (int, error) {
	g.read.Store(true)
	return g.ReadCloser.Read(p)
}

// Close is left to the owner of the original body: the server closes an inbound request body when its handler
// returns.
func (g *replayGuard) Close() error { return nil }

func withReplayGuard(req *http.Request) (*http.Request, *replayGuard) {
	if req.Body == nil || req.Body == http.NoBody {
		return req, nil
	}
	guard := &replayGuard{ReadCloser: req.Body}
	attempt := new(http.Request)
	*attempt = *req
	attempt.Body = guard
	return attempt, guard
}

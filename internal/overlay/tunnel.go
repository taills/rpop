package overlay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rpop-project/rpop/internal/pki"
)

// Headers of the relay protocol.
const (
	// RouteHeader names the relay route a tunnel follows.
	RouteHeader = "Rpop-Route"
	// ErrorHeader explains why a relay refused or could not forward a tunnel.
	ErrorHeader = "Rpop-Error"
	pingPath    = "/_rpop/ping"
)

// RelayError is a relay's refusal to forward a tunnel.
type RelayError struct {
	Peer   string
	Status int
	Reason string
}

func (e *RelayError) Error() string {
	return fmt.Sprintf("relay %s answered %d: %s", e.Peer, e.Status, e.Reason)
}

// tunnelTrace carries what tunnelConn needs to report its own StageEnded event when it closes. The zero value
// disables it: a relay reuses tunnel() to reach further hops but tracks that hop's lifecycle itself, around its
// call to dialNext, instead of letting the nested connection report its own end (see dialNext).
type tunnelTrace struct {
	tunnelID, nodeID, role string
	events                 *eventQueue
	opened                 time.Time
}

// openTunnel opens a CONNECT stream on a connection the caller reserved. The stream outlives ctx, which only
// bounds how long the relay may take to connect the next hop. On success it also returns the relay's
// Rpop-Protocol-Version response header (D27), or "" if the relay predates it, so the caller can compare it
// against its own version without a second round trip.
func openTunnel(ctx context.Context, cc *http.ClientConn, peer string, header http.Header, trace tunnelTrace) (net.Conn, string, error) {
	reader, writer := io.Pipe()
	streamCtx, cancel := context.WithCancel(context.Background())
	name := pki.NodeName(peer)
	request := (&http.Request{
		Method: http.MethodConnect, URL: &url.URL{Scheme: "https", Host: name}, Host: name,
		Header: header, Body: reader, ContentLength: -1,
	}).WithContext(streamCtx)
	type result struct {
		response *http.Response
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := cc.RoundTrip(request)
		done <- result{response, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			cancel()
			writer.CloseWithError(r.err)
			return nil, "", r.err
		}
		if r.response.StatusCode != http.StatusOK {
			reason := r.response.Header.Get(ErrorHeader)
			r.response.Body.Close()
			cancel()
			writer.Close()
			return nil, "", &RelayError{Peer: peer, Status: r.response.StatusCode, Reason: reason}
		}
		peerVersion := r.response.Header.Get(ProtocolVersionHeader)
		return &tunnelConn{body: r.response.Body, writer: writer, cancel: cancel, peer: peer, trace: trace}, peerVersion, nil
	case <-ctx.Done():
		cancel()
		writer.CloseWithError(ctx.Err())
		go func() {
			if r := <-done; r.err == nil {
				r.response.Body.Close()
			}
		}()
		return nil, "", ctx.Err()
	}
}

// tunnelConn is one CONNECT stream seen as a connection.
type tunnelConn struct {
	body   io.ReadCloser
	writer *io.PipeWriter
	cancel context.CancelFunc
	peer   string
	once   sync.Once
	// trace is set only for the connection the entry hop itself hands to the data plane; see tunnelTrace.
	trace             tunnelTrace
	bytesIn, bytesOut atomic.Uint64

	mu                    sync.Mutex
	readTimer, writeTimer *time.Timer
}

func (c *tunnelConn) Read(p []byte) (int, error) {
	n, err := c.body.Read(p)
	if n > 0 && c.trace.events != nil {
		c.bytesIn.Add(uint64(n))
	}
	return n, err
}

func (c *tunnelConn) Write(p []byte) (int, error) {
	n, err := c.writer.Write(p)
	if n > 0 && c.trace.events != nil {
		c.bytesOut.Add(uint64(n))
	}
	return n, err
}

// TunnelID reports the tunnel this connection belongs to, or "" when the tunnel does not log lifecycle events.
// The data plane reads it through httptrace.GotConn to tag its access log record (D22).
func (c *tunnelConn) TunnelID() string { return c.trace.tunnelID }

// CloseWrite ends the stream in the sending direction while responses keep arriving.
func (c *tunnelConn) CloseWrite() error { return c.writer.Close() }

func (c *tunnelConn) Close() error {
	c.once.Do(func() {
		c.writer.Close()
		c.body.Close()
		c.cancel()
		c.mu.Lock()
		stopTimer(c.readTimer)
		stopTimer(c.writeTimer)
		c.mu.Unlock()
		if c.trace.events != nil {
			c.trace.events.record(TunnelEvent{
				Timestamp: time.Now(), TunnelID: c.trace.tunnelID, NodeID: c.trace.nodeID, Role: c.trace.role, Stage: StageEnded,
				Peer: c.peer, BytesIn: c.bytesIn.Load(), BytesOut: c.bytesOut.Load(), Duration: time.Since(c.trace.opened),
			})
		}
	})
	return nil
}

func (c *tunnelConn) LocalAddr() net.Addr  { return tunnelAddr("local") }
func (c *tunnelConn) RemoteAddr() net.Addr { return tunnelAddr(c.peer) }

func (c *tunnelConn) SetDeadline(t time.Time) error {
	_ = c.SetReadDeadline(t)
	return c.SetWriteDeadline(t)
}

// SetReadDeadline closes the tunnel when the deadline passes. Callers set deadlines on overlay connections only
// to abandon them, so a tunnel does not need to stay usable afterwards.
func (c *tunnelConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readTimer = c.expireAt(c.readTimer, t)
	return nil
}

func (c *tunnelConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeTimer = c.expireAt(c.writeTimer, t)
	return nil
}

func (c *tunnelConn) expireAt(timer *time.Timer, t time.Time) *time.Timer {
	stopTimer(timer)
	if t.IsZero() {
		return nil
	}
	return time.AfterFunc(time.Until(t), func() { c.Close() })
}

func stopTimer(timer *time.Timer) {
	if timer != nil {
		timer.Stop()
	}
}

type tunnelAddr string

func (a tunnelAddr) Network() string { return "rpop-tunnel" }
func (a tunnelAddr) String() string  { return string(a) }

var errLinkDown = errors.New("link is down")

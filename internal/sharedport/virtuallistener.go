package sharedport

import (
	"errors"
	"net"
	"sync"
	"time"
)

// errListenerClosed is returned by virtualListener.Accept once the listener is closed.
var errListenerClosed = errors.New("sharedport: listener closed")

// deliverTimeout bounds how long deliver waits for a destination's Accept loop to keep up before giving up and
// closing the connection. Destinations run a tight accept-and-spawn-goroutine loop, so this only ever matters if
// one is genuinely stuck; it exists so a wedged destination cannot block the shared accept loop that feeds every
// other participant of the same port forever.
const deliverTimeout = 5 * time.Second

// virtualListener is a net.Listener fed by a channel instead of the kernel: the port's dispatch loop delivers
// connections it has already classified (and, for TLS, already handshaked) into it, and the destination's own
// *http.Server (or the port's own per-encoding meta server) drives it exactly like a real listener, including
// automatic HTTP/2 negotiation (see registry.go's doc comment on why TLSConfig is left nil on those servers).
type virtualListener struct {
	addr   net.Addr
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newVirtualListener(addr net.Addr) *virtualListener {
	return &virtualListener{addr: addr, conns: make(chan net.Conn, 64), closed: make(chan struct{})}
}

func (l *virtualListener) Accept() (net.Conn, error) {
	select {
	case conn, ok := <-l.conns:
		if !ok {
			return nil, errListenerClosed
		}
		return conn, nil
	case <-l.closed:
		return nil, errListenerClosed
	}
}

func (l *virtualListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *virtualListener) Addr() net.Addr { return l.addr }

// deliver hands conn to whatever is calling Accept. It reports whether the connection was accepted; the caller
// closes conn itself when deliver returns false (listener closed, or the destination did not keep up within
// deliverTimeout).
func (l *virtualListener) deliver(conn net.Conn) bool {
	select {
	case <-l.closed:
		return false
	default:
	}
	timer := time.NewTimer(deliverTimeout)
	defer timer.Stop()
	select {
	case l.conns <- conn:
		return true
	case <-l.closed:
		return false
	case <-timer.C:
		return false
	}
}

package sharedport

import (
	"net"
	"syscall"
)

// notSentLowat caps the unsent bytes the kernel queues per socket. Without it a bulk transfer fills the send
// buffer and a small, latency-sensitive frame (an SSE token, a WebSocket message, a relay tunnel's interactive
// frame) multiplexed on the same connection waits behind megabytes; with it the writer keeps the backlog in user
// space, where frames interleave. Every connection a shared listener accepts gets this treatment, matching (and
// slightly extending, to plaintext dataplane traffic) what internal/overlay's tunedListener already applied to
// every relay connection before the relay port started sharing listeners through this package.
//
// This duplicates internal/overlay/sockopt.go's tuneConn (deliberately: this package must not import overlay, so
// its relay port can register with it, see the package doc comment).
const notSentLowat = 16 << 10

// tuneAcceptedConn applies the TCP_NOTSENT_LOWAT socket option to a freshly accepted connection.
func tuneAcceptedConn(conn net.Conn) {
	raw, ok := conn.(syscall.Conn)
	if !ok {
		return
	}
	rc, err := raw.SyscallConn()
	if err != nil {
		return
	}
	// The option is an optimization; kernels without it still carry traffic correctly.
	_ = rc.Control(func(fd uintptr) { _ = setNotSentLowat(fd, notSentLowat) })
}

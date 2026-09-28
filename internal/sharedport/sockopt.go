package sharedport

import (
	"net"
	"syscall"
)

// notSentLowat caps the unsent bytes the kernel queues per socket. Without it a bulk transfer fills the send
// buffer and a small, latency-sensitive frame (an SSE token, a WebSocket message, a relay tunnel's interactive
// frame) multiplexed on the same connection waits behind megabytes; with it the writer keeps the backlog in user
// space, where frames interleave. Every connection a shared listener accepts gets this treatment, matching (and
// slightly extending, to plaintext dataplane traffic) what internal/overlay's tunedListener used to apply to
// every relay connection before the relay port started sharing listeners through this package.
//
// This used to duplicate internal/overlay/sockopt.go's tuneConn, back when the relay port ran its own listener
// (deliberately: this package must not import overlay, so its relay port can register with it, see the package
// doc comment); stage two of shared-port removed that now-redundant copy from overlay once the relay port
// started registering here instead, since every connection it accepts already gets this same treatment before
// dispatch.go even classifies it as TLS or plaintext.
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

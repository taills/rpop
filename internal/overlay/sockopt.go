package overlay

import (
	"syscall"
)

// notSentLowat caps the unsent bytes the kernel queues per socket. Without it a bulk transfer fills the send
// buffer and a small, latency-sensitive frame (an SSE token, a WebSocket message) multiplexed on the same link
// waits behind megabytes; with it the HTTP/2 writer keeps the backlog in user space, where frames interleave.
//
// This only tunes outbound (dialed) connections now — tuneSocket, via tcpDialer's Control func. The relay port's
// own accepted connections used to get the same treatment here too (tuneConn, applied by relay.go's
// tunedListener), but stage two of shared-port moved the relay port onto internal/sharedport.Registry
// (PutTLSOwner), whose own accept loop already applies the identical option to every connection it accepts —
// the relay port's included — before dispatch.go even classifies it as TLS or plaintext (see
// internal/sharedport/sockopt.go's tuneAcceptedConn); duplicating it here for relay connections specifically
// would only tune the same socket twice.
const notSentLowat = 16 << 10

// tuneSocket is a net.Dialer Control function for overlay sockets.
func tuneSocket(_, _ string, c syscall.RawConn) error {
	var sockErr error
	if err := c.Control(func(fd uintptr) { sockErr = setNotSentLowat(fd, notSentLowat) }); err != nil {
		return err
	}
	// The option is an optimization; kernels without it still carry traffic correctly.
	_ = sockErr
	return nil
}

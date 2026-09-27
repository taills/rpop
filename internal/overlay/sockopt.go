package overlay

import (
	"net"
	"syscall"
)

// notSentLowat caps the unsent bytes the kernel queues per socket. Without it a bulk transfer fills the send
// buffer and a small, latency-sensitive frame (an SSE token, a WebSocket message) multiplexed on the same link
// waits behind megabytes; with it the HTTP/2 writer keeps the backlog in user space, where frames interleave.
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

// tuneConn applies tuneSocket to an accepted connection.
func tuneConn(conn net.Conn) {
	if raw, ok := conn.(syscall.Conn); ok {
		if c, err := raw.SyscallConn(); err == nil {
			_ = tuneSocket("", "", c)
		}
	}
}

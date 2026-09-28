package sharedport

import "syscall"

// tcpNotSentLowat is TCP_NOTSENT_LOWAT from linux/tcp.h.
const tcpNotSentLowat = 25

func setNotSentLowat(fd uintptr, bytes int) error {
	return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpNotSentLowat, bytes)
}

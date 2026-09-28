package sharedport

import "syscall"

// tcpNotSentLowat is TCP_NOTSENT_LOWAT from netinet/tcp.h.
const tcpNotSentLowat = 0x201

func setNotSentLowat(fd uintptr, bytes int) error {
	return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpNotSentLowat, bytes)
}

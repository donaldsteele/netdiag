package portprobe

import (
	"errors"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sioTCPInitialRTO is SIO_TCP_INITIAL_RTO (_WSAIOW(IOC_VENDOR, 17)).
const sioTCPInitialRTO = 0x98000011

// tcpInitialRTOParameters mirrors TCP_INITIAL_RTO_PARAMETERS.
type tcpInitialRTOParameters struct {
	Rtt                   uint16
	MaxSynRetransmissions uint8
}

const (
	rtoUnspecifiedRtt   = 0xffff // TCP_INITIAL_RTO_UNSPECIFIED_RTT: keep the default
	rtoNoSynRetransmits = 0xfe   // TCP_INITIAL_RTO_NO_SYN_RETRANSMISSIONS
)

// fastRefuse turns off SYN retransmission on the socket. Windows answers a
// reset to its SYN by sending the SYN again twice, so a closed port on
// another host takes about 2 s to report "refused", longer than the probe
// timeouts. Without retransmission the reset is reported at once. If the
// ioctl fails the dial simply behaves as before.
func fastRefuse(_, _ string, c syscall.RawConn) error {
	return c.Control(func(fd uintptr) { _ = noSynRetransmits(fd) })
}

func noSynRetransmits(fd uintptr) error {
	p := tcpInitialRTOParameters{Rtt: rtoUnspecifiedRtt, MaxSynRetransmissions: rtoNoSynRetransmits}
	var n uint32
	return windows.WSAIoctl(windows.Handle(fd), sioTCPInitialRTO,
		(*byte)(unsafe.Pointer(&p)), uint32(unsafe.Sizeof(p)), nil, 0, &n, nil, 0)
}

// refused checks WSAECONNREFUSED (10061): on Windows syscall.ECONNREFUSED is
// a different, invented errno that a dial never returns.
func refused(err error) bool {
	return errors.Is(err, windows.WSAECONNREFUSED)
}

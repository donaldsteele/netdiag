package portprobe

import (
	"context"
	"net"
	"syscall"
	"testing"
	"time"
)

// TestNoSynRetransmitsAccepted checks that Windows accepts the
// SIO_TCP_INITIAL_RTO parameters fastRefuse sets (fastRefuse itself
// ignores the error).
func TestNoSynRetransmitsAccepted(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var ioctlErr error
	d := net.Dialer{Control: func(_, _ string, c syscall.RawConn) error {
		return c.Control(func(fd uintptr) { ioctlErr = noSynRetransmits(fd) })
	}}
	c, err := d.DialContext(context.Background(), "tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if ioctlErr != nil {
		t.Fatalf("SIO_TCP_INITIAL_RTO: %v", ioctlErr)
	}
}

// TestDialRefusedFastOffLoopback guards fastRefuse itself. Windows (and Go)
// refuse loopback dials at once anyway, but a closed port on this
// computer's own LAN address takes about 2 s to refuse without it, longer
// than ConnectTimeout, so the dial would time out instead.
func TestDialRefusedFastOffLoopback(t *testing.T) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var ip net.IP
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			ip = n.IP
			break
		}
	}
	if ip == nil {
		t.Skip("no non-loopback IPv4 address")
	}
	l, err := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
	if err != nil {
		t.Skip(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	t0 := time.Now()
	_, err = Dial(context.Background(), ip.String(), port, ConnectTimeout)
	if d := time.Since(t0); !Refused(err) || d > 500*time.Millisecond {
		t.Fatalf("%s:%d: refused=%v after %v (%v)", ip, port, Refused(err), d, err)
	}
}

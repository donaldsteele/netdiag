package portprobe

import (
	"context"
	"net"
	"strconv"
	"time"
)

// fastRefuseMax is the longest timeout for which Dial turns on fastRefuse.
// It matches the Windows default initial retransmission timeout (1 s): a
// shorter dial ends before the first SYN retransmission, so turning
// retransmission off changes nothing for open or silent ports.
const fastRefuseMax = time.Second

// Dial makes one TCP connection to ip:port. Use Refused on the error to tell
// a closed port (the host answered with a reset) from a silent one.
func Dial(ctx context.Context, ip string, port int, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: timeout}
	if timeout > 0 && timeout <= fastRefuseMax {
		d.Control = fastRefuse
	}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
}

// Refused reports whether a Dial error means the connection was actively
// refused, which proves the host is up.
func Refused(err error) bool {
	return err != nil && refused(err)
}

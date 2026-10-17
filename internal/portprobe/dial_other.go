//go:build !windows

package portprobe

import (
	"errors"
	"syscall"
)

// fastRefuse is only needed on Windows; elsewhere a reset is reported at once.
var fastRefuse func(network, address string, c syscall.RawConn) error

func refused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}

//go:build !windows && !darwin

package tailscale

import (
	"context"
	"io"
	"net"
	"time"
)

// platformDial uses tailscaled's Unix socket (netdiag ships for Windows and
// macOS; this keeps other builds and tests working).
func platformDial(ctx context.Context) (io.ReadWriteCloser, string, error) {
	d := net.Dialer{Timeout: 2 * time.Second}
	for _, sock := range []string{"/var/run/tailscale/tailscaled.sock", "/var/run/tailscaled.socket"} {
		if c, err := d.DialContext(ctx, "unix", sock); err == nil {
			return c, "", nil
		}
	}
	return nil, "", ErrNotInstalled
}

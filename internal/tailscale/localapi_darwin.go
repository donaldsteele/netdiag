//go:build darwin

package tailscale

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// platformDial finds the Tailscale service the way the Tailscale command
// line does on macOS:
//   - the open-source tailscaled's Unix socket;
//   - the standalone Tailscale app: /Library/Tailscale/ipnport (a symlink
//     whose target is the port) and the token in sameuserproof-<port>;
//   - the App Store app: a sameuserproof-<port>-<token> file in its group
//     container.
//
// The app variants answer on 127.0.0.1 with the token as the password.
func platformDial(ctx context.Context) (io.ReadWriteCloser, string, error) {
	var d net.Dialer
	d.Timeout = 2 * time.Second
	for _, sock := range []string{"/var/run/tailscaled.socket", "/var/run/tailscale/tailscaled.sock"} {
		if fi, err := os.Stat(sock); err == nil && fi.Mode()&os.ModeSocket != 0 {
			if c, err := d.DialContext(ctx, "unix", sock); err == nil {
				return c, "", nil
			}
		}
	}
	if port, token, ok := standalonePortToken(); ok {
		if c, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", port)); err == nil {
			return c, token, nil
		}
	}
	if port, token, ok := appStorePortToken(); ok {
		if c, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", port)); err == nil {
			return c, token, nil
		}
	}
	return nil, "", ErrNotInstalled
}

func standalonePortToken() (port, token string, ok bool) {
	target, err := os.Readlink("/Library/Tailscale/ipnport")
	if err != nil {
		return "", "", false
	}
	if _, err := strconv.Atoi(target); err != nil {
		return "", "", false
	}
	b, err := os.ReadFile("/Library/Tailscale/sameuserproof-" + target)
	if err != nil {
		return "", "", false
	}
	return target, strings.TrimSpace(string(b)), true
}

func appStorePortToken() (port, token string, ok bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", false
	}
	matches, _ := filepath.Glob(filepath.Join(home, "Library", "Group Containers", "*.io.tailscale.ipn.macos", "sameuserproof-*"))
	more, _ := filepath.Glob(filepath.Join(home, "Library", "Group Containers", "io.tailscale.ipn.macos", "sameuserproof-*"))
	for _, m := range append(matches, more...) {
		rest := strings.TrimPrefix(filepath.Base(m), "sameuserproof-")
		p, tok, found := strings.Cut(rest, "-")
		if !found || tok == "" {
			continue
		}
		if _, err := strconv.Atoi(p); err == nil {
			return p, tok, true
		}
	}
	return "", "", false
}

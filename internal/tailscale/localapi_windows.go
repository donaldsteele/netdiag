//go:build windows

package tailscale

import (
	"context"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/windows"
)

// pipeName is the Tailscale service's LocalAPI pipe. "ProtectedPrefix\Administrators"
// means only an administrator process (the service) can create it, so a
// standard-user program cannot pose as Tailscale.
const pipeName = `\\.\pipe\ProtectedPrefix\Administrators\Tailscale\tailscaled`

// platformDial opens the pipe for identification only (the level the
// Tailscale command line uses): the service learns who is asking, nothing more.
func platformDial(context.Context) (io.ReadWriteCloser, string, error) {
	p, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		return nil, "", err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return nil, "", ErrNotInstalled
		}
		return nil, "", err
	}
	return os.NewFile(uintptr(h), pipeName), "", nil
}

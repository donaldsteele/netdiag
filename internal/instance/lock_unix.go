//go:build !windows

package instance

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// lockFile is the file the flock is held on, in the data folder.
const lockFile = "instance.lock"

// sysLock is an exclusive flock on <datadir>/instance.lock, held on an open
// file until netdiag exits.
type sysLock struct{ f *os.File }

func acquire(dir, _ string) (sysLock, error) {
	f, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return sysLock{}, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return sysLock{}, ErrLocked
		}
		return sysLock{}, err
	}
	return sysLock{f: f}, nil
}

func (s sysLock) release() {
	if s.f != nil {
		_ = unix.Flock(int(s.f.Fd()), unix.LOCK_UN)
		s.f.Close()
	}
}

package instance

import (
	"errors"

	"golang.org/x/sys/windows"
)

// sysLock is a named mutex in this sign-in session's namespace
// (Local\netdiag-<hash of the data folder>). netdiag only keeps a handle
// open: a mutex that already exists means another netdiag holds it.
type sysLock struct{ h windows.Handle }

func acquire(_, key string) (sysLock, error) {
	name, err := windows.UTF16PtrFromString(`Local\netdiag-` + key)
	if err != nil {
		return sysLock{}, err
	}
	h, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
		return sysLock{}, ErrLocked
	}
	if err != nil {
		return sysLock{}, err
	}
	return sysLock{h: h}, nil
}

func (s sysLock) release() {
	if s.h != 0 {
		_ = windows.CloseHandle(s.h)
	}
}

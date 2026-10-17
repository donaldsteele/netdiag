//go:build !windows && !darwin

package remote

import (
	"net/netip"
	"runtime"
)

func platformCaps() Caps { return Caps{Platform: runtime.GOOS} }

func platformOpen(Kind, netip.Addr, int) error { return ErrNoApp }

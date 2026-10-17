package remote

import (
	"net/netip"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"netdiag/internal/model"
)

// mstsc is Remote Desktop Connection, part of every Windows edition.
func mstsc() string {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		return ""
	}
	p := filepath.Join(dir, "mstsc.exe")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// vncHandler reports whether a program (a VNC viewer) handles vnc: links.
// Windows has no VNC viewer of its own.
func vncHandler() bool {
	k, err := registry.OpenKey(registry.CLASSES_ROOT, `vnc\shell\open\command`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

func platformCaps() Caps {
	c := Caps{Platform: "windows"}
	if mstsc() != "" {
		c.RDP, c.RDPApp = true, "Remote Desktop Connection"
	}
	if vncHandler() {
		c.VNC, c.VNCApp = true, "your VNC viewer"
	}
	return c
}

// platformOpen starts the program with ShellExecute, the way Explorer would;
// netdiag does not wait for it or read its output.
func platformOpen(k Kind, a netip.Addr, port int) error {
	switch k {
	case RDP:
		exe := mstsc()
		if exe == "" {
			return ErrNoApp
		}
		return shellExecute(exe, "/v:"+hostPort(a, port, model.PortRDP))
	case VNC:
		if !vncHandler() {
			return ErrNoApp
		}
		return shellExecute("vnc://"+netip.AddrPortFrom(a, uint16(port)).String(), "")
	}
	return ErrNotOpenable
}

func shellExecute(file, args string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	f, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	var p *uint16
	if args != "" {
		if p, err = windows.UTF16PtrFromString(args); err != nil {
			return err
		}
	}
	return windows.ShellExecute(0, verb, f, p, nil, windows.SW_SHOWNORMAL)
}

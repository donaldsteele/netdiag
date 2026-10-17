package remote

import (
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// rdpApps are Microsoft's macOS clients for .rdp files, newest name first.
var rdpApps = []string{"Windows App.app", "Microsoft Remote Desktop.app"}

func rdpApp() string {
	dirs := []string{"/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Applications"))
	}
	for _, app := range rdpApps {
		for _, d := range dirs {
			if _, err := os.Stat(filepath.Join(d, app)); err == nil {
				return app[:len(app)-len(".app")]
			}
		}
	}
	return ""
}

func platformCaps() Caps {
	c := Caps{Platform: "darwin"}
	if app := rdpApp(); app != "" {
		c.RDP, c.RDPApp = true, app
	}
	// Screen Sharing ships with every macOS and owns vnc: links.
	c.VNC, c.VNCApp = true, "Screen Sharing"
	return c
}

// platformOpen hands the session to macOS with open(1): a temporary .rdp
// file for Remote Desktop, a vnc: link for Screen Sharing.
func platformOpen(k Kind, a netip.Addr, port int) error {
	switch k {
	case RDP:
		app := rdpApp()
		if app == "" {
			return ErrNoApp
		}
		removeOldRDPFiles()
		b, err := RDPFile(a.String(), port)
		if err != nil {
			return err
		}
		f, err := os.CreateTemp("", "netdiag-*.rdp")
		if err != nil {
			return err
		}
		name := f.Name()
		_, werr := f.Write(b)
		if err := f.Close(); werr == nil {
			werr = err
		}
		if werr != nil {
			os.Remove(name)
			return werr
		}
		// The app reads the file after open returns; remove it later (and
		// on the next open, if netdiag quit first).
		time.AfterFunc(2*time.Minute, func() { os.Remove(name) })
		return run("/usr/bin/open", "-a", app, name)
	case VNC:
		return run("/usr/bin/open", "vnc://"+netip.AddrPortFrom(a, uint16(port)).String())
	}
	return ErrNotOpenable
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil && len(out) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return err
}

// removeOldRDPFiles deletes connection files left by an earlier run.
func removeOldRDPFiles() {
	old, _ := filepath.Glob(filepath.Join(os.TempDir(), "netdiag-*.rdp"))
	for _, f := range old {
		if fi, err := os.Stat(f); err == nil && time.Since(fi.ModTime()) > 10*time.Minute {
			os.Remove(f)
		}
	}
}

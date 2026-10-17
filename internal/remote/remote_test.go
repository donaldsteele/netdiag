package remote

import (
	"net/netip"
	"strings"
	"testing"
)

func TestRDPFile(t *testing.T) {
	b, err := RDPFile("192.168.1.20", 3389)
	if err != nil {
		t.Fatal(err)
	}
	if want := "full address:s:192.168.1.20\r\nprompt for credentials:i:1\r\nscreen mode id:i:2\r\n"; string(b) != want {
		t.Fatalf("got %q want %q", b, want)
	}
	if b, _ := RDPFile("192.168.1.20", 3390); !strings.HasPrefix(string(b), "full address:s:192.168.1.20:3390\r\n") {
		t.Fatalf("other port: %q", b)
	}
	for _, bad := range []string{"", "pc.local", "192.168.1.20\r\nalternate shell:s:cmd", "fe80::1%13"} {
		if _, err := RDPFile(bad, 3389); err == nil {
			t.Errorf("RDPFile(%q): no error", bad)
		}
	}
}

func TestHostPort(t *testing.T) {
	for _, tc := range []struct {
		ip        string
		port, def int
		want      string
	}{
		{"10.0.0.5", 3389, 3389, "10.0.0.5"},
		{"10.0.0.5", 3390, 3389, "10.0.0.5:3390"},
		{"fd00::5", 3389, 3389, "fd00::5"},
		{"fd00::5", 5901, 5900, "[fd00::5]:5901"},
	} {
		if got := hostPort(netip.MustParseAddr(tc.ip), tc.port, tc.def); got != tc.want {
			t.Errorf("hostPort(%s, %d) = %q, want %q", tc.ip, tc.port, got, tc.want)
		}
	}
}

// TestOpenRejectsBadInput checks the arguments before anything is started.
func TestOpenRejectsBadInput(t *testing.T) {
	var s System
	for _, tc := range []struct {
		k    Kind
		ip   string
		port int
	}{
		{RDP, "pc.local", 3389},
		{RDP, "10.0.0.5 /admin", 3389},
		{RDP, "fe80::1%13", 3389},
		{RDP, "10.0.0.5", 0},
		{VNC, "10.0.0.5", 70000},
		{RDP, "203.0.113.7", 3389},
		{RDP, "8.8.8.8", 3389},
	} {
		if err := s.Open(tc.k, tc.ip, tc.port); err == nil || err == ErrNoApp {
			t.Errorf("Open(%s, %q, %d) = %v, want an input error", tc.k, tc.ip, tc.port, err)
		}
	}
	if err := s.Open(SSH, "10.0.0.5", 22); err != ErrNotOpenable {
		t.Errorf("Open(ssh) = %v, want ErrNotOpenable", err)
	}
	if err := s.Open("telnet", "10.0.0.5", 23); err != ErrNotOpenable {
		t.Errorf("Open(telnet) = %v, want ErrNotOpenable", err)
	}
}

func TestLocal(t *testing.T) {
	for ip, want := range map[string]bool{"192.168.1.5": true, "10.1.2.3": true, "172.16.0.9": true,
		"169.254.10.1": true, "100.64.1.1": true, "fd00::5": true, "fe80::1": true,
		"8.8.8.8": false, "203.0.113.7": false, "127.0.0.1": false, "2001:4860::8888": false} {
		if Local(netip.MustParseAddr(ip)) != want {
			t.Errorf("Local(%s) = %v", ip, !want)
		}
	}
}

func TestPort(t *testing.T) {
	if Port(RDP) != 3389 || Port(VNC) != 5900 || Port(SSH) != 22 || Port("x") != 0 {
		t.Fatal("ports")
	}
}

func TestCapsPlatform(t *testing.T) {
	c := System{}.Caps()
	if c.Platform == "" {
		t.Fatal("no platform")
	}
	if c.RDP && c.RDPApp == "" || c.VNC && c.VNCApp == "" {
		t.Fatalf("app name missing: %+v", c)
	}
	t.Logf("%+v", c)
}

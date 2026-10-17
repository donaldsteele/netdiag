package health

import (
	"context"
	"net"
	"net/netip"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestReadDNSServersLive reads the DNS servers of every IPv4 address of this
// computer. It only checks that each adapter is found and the list parses.
func TestReadDNSServersLive(t *testing.T) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.To4() == nil || ipn.IP.IsLoopback() {
			continue
		}
		n++
		servers, err := readDNSServers(context.Background(), "", ipn.IP.String())
		if err != nil {
			t.Errorf("%s: %v", ipn.IP, err)
			continue
		}
		for _, s := range servers {
			if _, err := netip.ParseAddr(s); err != nil {
				t.Errorf("%s: bad server %q", ipn.IP, s)
			}
		}
		t.Logf("%s: %v", ipn.IP, servers)
	}
	if n == 0 {
		t.Skip("no IPv4 address")
	}
	if _, err := readDNSServers(context.Background(), "", "203.0.113.9"); err == nil {
		t.Error("unknown address: no error")
	}
}

func TestDNSServerStringZone(t *testing.T) {
	mk6 := func(ip string, scope uint32) windows.SocketAddress {
		var raw windows.RawSockaddrInet6
		raw.Family = windows.AF_INET6
		raw.Addr = netip.MustParseAddr(ip).As16()
		raw.Scope_id = scope
		return windows.SocketAddress{Sockaddr: (*syscall.RawSockaddrAny)(unsafe.Pointer(&raw)), SockaddrLength: int32(unsafe.Sizeof(raw))}
	}
	ll := mk6("fe80::1", 13)
	if got := dnsServerString(&ll); got != "fe80::1%13" {
		t.Errorf("link-local: %q", got)
	}
	site := mk6("fec0:0:0:ffff::1", 1)
	if got := dnsServerString(&site); got != "fec0:0:0:ffff::1" {
		t.Errorf("site-local: %q", got)
	}
	var raw4 windows.RawSockaddrInet4
	raw4.Family = windows.AF_INET
	raw4.Addr = [4]byte{192, 168, 1, 1}
	v4 := windows.SocketAddress{Sockaddr: (*syscall.RawSockaddrAny)(unsafe.Pointer(&raw4)), SockaddrLength: int32(unsafe.Sizeof(raw4))}
	if got := dnsServerString(&v4); got != "192.168.1.1" {
		t.Errorf("IPv4: %q", got)
	}
}

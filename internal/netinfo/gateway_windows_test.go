//go:build windows

package netinfo

import (
	"net/netip"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func fwdRow(idx uint32, dst string, bits uint8, hop string, metric uint32) windows.MibIpForwardRow2 {
	var r windows.MibIpForwardRow2
	r.InterfaceIndex = idx
	r.Metric = metric
	d := (*windows.RawSockaddrInet4)(unsafe.Pointer(&r.DestinationPrefix.Prefix))
	d.Family, d.Addr = windows.AF_INET, netip.MustParseAddr(dst).As4()
	r.DestinationPrefix.PrefixLength = bits
	h := (*windows.RawSockaddrInet4)(unsafe.Pointer(&r.NextHop))
	h.Family, h.Addr = windows.AF_INET, netip.MustParseAddr(hop).As4()
	return r
}

func TestBestDefaultRoute(t *testing.T) {
	// Interface metrics: 8 Hyper-V (5000), 12 Wi-Fi (35), 20 VPN, 30 down.
	metrics := map[uint32]uint32{8: 5000, 12: 35, 20: 1}
	ifMetric := func(idx uint32) (uint32, bool) { m, ok := metrics[idx]; return m, ok }
	iface := func(idx uint32, gw netip.Addr) (string, netip.Addr) {
		switch idx {
		case 8:
			return "vEthernet", netip.MustParseAddr("172.20.0.5")
		case 12:
			return "Wi-Fi", netip.MustParseAddr("192.168.1.50")
		case 20:
			return "VPN", netip.MustParseAddr("10.8.0.2")
		}
		return "Down", netip.Addr{}
	}
	rows := []windows.MibIpForwardRow2{
		fwdRow(8, "0.0.0.0", 0, "172.20.0.1", 0),
		fwdRow(12, "192.168.1.0", 24, "0.0.0.0", 0), // not a default route
		fwdRow(20, "0.0.0.0", 0, "0.0.0.0", 0),      // on-link default
		fwdRow(30, "0.0.0.0", 0, "192.168.9.1", 0),  // interface not connected
		fwdRow(12, "0.0.0.0", 0, "192.168.1.1", 0),
		fwdRow(8, "0.0.0.0", 0, "172.20.0.254", 0), // loses on metric
	}
	rt, err := bestDefaultRoute(rows, ifMetric, iface)
	if err != nil {
		t.Fatal(err)
	}
	want := Route{Gateway: netip.MustParseAddr("192.168.1.1"), IfaceIP: netip.MustParseAddr("192.168.1.50"), IfaceName: "Wi-Fi"}
	if rt != want {
		t.Fatalf("got %+v want %+v", rt, want)
	}
	// Route metric counts too: 35+5000 on Wi-Fi loses to 5000+0 on Hyper-V.
	rows[4].Metric = 5000
	if rt, _ = bestDefaultRoute(rows, ifMetric, iface); rt.Gateway.String() != "172.20.0.1" {
		t.Fatalf("got %+v want gateway 172.20.0.1", rt)
	}
	if _, err := bestDefaultRoute(rows[1:4], ifMetric, iface); err == nil {
		t.Fatal("expected error")
	}
}

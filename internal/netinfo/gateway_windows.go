//go:build windows

package netinfo

import (
	"errors"
	"net"
	"net/netip"
	"slices"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DefaultRoute returns the lowest-metric default IPv4 route, read with
// GetIpForwardTable2 (the table `route print` shows) instead of running
// route.exe.
func DefaultRoute() (Route, error) {
	var tab *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(windows.AF_INET, &tab); err != nil {
		return Route{}, err
	}
	rows := slices.Clone(tab.Rows())
	windows.FreeMibTable(unsafe.Pointer(tab))
	return bestDefaultRoute(rows, interfaceMetric, routeIface)
}

// bestDefaultRoute picks the 0.0.0.0/0 row with a gateway and the lowest
// route plus interface metric (the Metric column of `route print`); the
// first row wins a tie. ifMetric reports the interface metric, false for an
// interface that is not connected. iface names the interface and picks its
// address for the gateway; rows whose interface has no IPv4 are skipped.
func bestDefaultRoute(rows []windows.MibIpForwardRow2,
	ifMetric func(idx uint32) (uint32, bool),
	iface func(idx uint32, gw netip.Addr) (name string, ip netip.Addr),
) (Route, error) {
	var best uint64
	var rt Route
	found := false
	for i := range rows {
		row := &rows[i]
		dst := (*windows.RawSockaddrInet4)(unsafe.Pointer(&row.DestinationPrefix.Prefix))
		hop := (*windows.RawSockaddrInet4)(unsafe.Pointer(&row.NextHop))
		if row.DestinationPrefix.PrefixLength != 0 || dst.Family != windows.AF_INET || hop.Family != windows.AF_INET {
			continue
		}
		gw := netip.AddrFrom4(hop.Addr)
		if gw.IsUnspecified() { // on-link default (some VPNs)
			continue
		}
		im, ok := ifMetric(row.InterfaceIndex)
		if !ok {
			continue
		}
		name, ip := iface(row.InterfaceIndex, gw)
		if !ip.IsValid() {
			continue
		}
		if m := uint64(row.Metric) + uint64(im); !found || m < best {
			best, rt, found = m, Route{Gateway: gw, IfaceIP: ip, IfaceName: name}, true
		}
	}
	if !found {
		return Route{}, errors.New("no default route found")
	}
	return rt, nil
}

// interfaceMetric returns the IPv4 metric of interface idx, false if it is
// not connected.
func interfaceMetric(idx uint32) (uint32, bool) {
	row := windows.MibIpInterfaceRow{Family: windows.AF_INET, InterfaceIndex: idx}
	if err := windows.GetIpInterfaceEntry(&row); err != nil || row.Connected == 0 {
		return 0, false
	}
	return row.Metric, true
}

// routeIface returns the name of interface idx and its IPv4 address on the
// gateway's subnet, else its first IPv4 address.
func routeIface(idx uint32, gw netip.Addr) (string, netip.Addr) {
	ifc, err := net.InterfaceByIndex(int(idx))
	if err != nil {
		return "", netip.Addr{}
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return ifc.Name, netip.Addr{}
	}
	var first netip.Addr
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipn.IP)
		if !ok || !ip.Unmap().Is4() {
			continue
		}
		ip = ip.Unmap()
		ones, _ := ipn.Mask.Size()
		if netip.PrefixFrom(ip, ones).Contains(gw) {
			return ifc.Name, ip
		}
		if !first.IsValid() {
			first = ip
		}
	}
	return ifc.Name, first
}

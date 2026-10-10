package mdns

import (
	"fmt"
	"net"
	"net/netip"

	"golang.org/x/net/ipv4"
)

// DialUDP opens an ephemeral-port IPv4 UDP socket (never 5353) whose
// multicast traffic leaves through ifaceName. ifaceName may be an interface
// name, an IPv4 address owned by an interface, or "" for the OS default.
// Setting the multicast interface and TTL is best effort.
func DialUDP(ifaceName string) (Transport, error) {
	ifi, local, err := resolveIface(ifaceName)
	if err != nil {
		return nil, err
	}
	laddr := &net.UDPAddr{IP: net.IPv4zero, Port: 0}
	if local.IsValid() {
		laddr.IP = net.IP(local.AsSlice())
	}
	c, err := net.ListenUDP("udp4", laddr)
	if err != nil {
		return nil, err
	}
	p := ipv4.NewPacketConn(c)
	if ifi != nil {
		_ = p.SetMulticastInterface(ifi)
	}
	_ = p.SetMulticastTTL(255)
	return c, nil
}

// resolveIface finds the interface and its first IPv4 address.
func resolveIface(name string) (*net.Interface, netip.Addr, error) {
	if name == "" {
		return nil, netip.Addr{}, nil
	}
	if ip, err := netip.ParseAddr(name); err == nil {
		ifs, err := net.Interfaces()
		if err != nil {
			return nil, netip.Addr{}, err
		}
		for i := range ifs {
			if a, ok := firstIPv4(&ifs[i], ip); ok {
				return &ifs[i], a, nil
			}
		}
		return nil, netip.Addr{}, fmt.Errorf("no interface owns %s", name)
	}
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil, netip.Addr{}, err
	}
	a, _ := firstIPv4(ifi, netip.Addr{})
	return ifi, a, nil
}

// firstIPv4 returns want if ifi owns it, or (when want is invalid) the first
// IPv4 address of ifi.
func firstIPv4(ifi *net.Interface, want netip.Addr) (netip.Addr, bool) {
	addrs, err := ifi.Addrs()
	if err != nil {
		return netip.Addr{}, false
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipn.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		if !ip.Is4() {
			continue
		}
		if !want.IsValid() || ip == want.Unmap() {
			return ip, true
		}
	}
	return netip.Addr{}, false
}

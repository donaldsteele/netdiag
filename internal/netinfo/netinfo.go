package netinfo

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
)

// Route is the default IPv4 route as reported by the OS.
type Route struct {
	Gateway   netip.Addr
	IfaceIP   netip.Addr // may be invalid if unknown
	IfaceName string     // may be empty if unknown
}

// Interfaces returns up, non-loopback, non-link-local IPv4 interfaces ranked
// by Rank. Default-route detection failure is tolerated.
func Interfaces() ([]Iface, error) {
	nics, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var list []Iface
	for _, n := range nics {
		if n.Flags&net.FlagUp == 0 || n.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := n.Addrs()
		if err != nil {
			continue
		}
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
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			ones, _ := ipn.Mask.Size()
			list = append(list, Iface{
				Name: n.Name,
				IP:   ip.String(),
				CIDR: netip.PrefixFrom(ip, ones).Masked().String(),
				MAC:  n.HardwareAddr.String(),
			})
		}
	}
	return Rank(list, defaultIfaceName(list)), nil
}

// defaultIfaceName resolves the default route to one of the given interfaces.
func defaultIfaceName(list []Iface) string {
	rt, err := DefaultRoute()
	if err != nil {
		return ""
	}
	for _, f := range list {
		if rt.IfaceName != "" && f.Name == rt.IfaceName {
			return f.Name
		}
		if rt.IfaceIP.IsValid() && f.IP == rt.IfaceIP.String() {
			return f.Name
		}
	}
	return ""
}

// SelfIP returns the IP of the default interface (or the best-ranked one).
func SelfIP() (string, error) {
	list, err := Interfaces()
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", errors.New("no usable IPv4 interface")
	}
	return list[0].IP, nil
}

// SuggestTarget returns the subnet of f as a scan target. If the subnet is
// larger than a /24, the /24 containing the interface IP is returned and
// truncated is true.
func SuggestTarget(f Iface) (cidr string, truncated bool) {
	p, err := netip.ParsePrefix(f.CIDR)
	if err != nil {
		return f.CIDR, false
	}
	if p.Bits() >= 24 {
		return p.Masked().String(), false
	}
	ip, err := netip.ParseAddr(f.IP)
	if err != nil {
		return p.Masked().String(), true
	}
	return fmt.Sprintf("%s/24", netip.PrefixFrom(ip, 24).Masked().Addr()), true
}

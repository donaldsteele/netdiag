package health

import (
	"context"
	"fmt"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
)

// readDNSServers lists the DNS servers configured on the adapter that owns
// ip, with GetAdaptersAddresses (the "DNS Servers" lines of ipconfig /all).
func readDNSServers(_ context.Context, _, ip string) ([]string, error) {
	const flags = windows.GAA_FLAG_SKIP_ANYCAST | windows.GAA_FLAG_SKIP_MULTICAST | windows.GAA_FLAG_SKIP_FRIENDLY_NAME
	size := uint32(16 << 10)
	for {
		buf := make([]byte, size)
		aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, aa, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("GetAdaptersAddresses: %w", err)
		}
		for a := aa; a != nil; a = a.Next {
			if !adapterAddrHasIP(a, ip) {
				continue
			}
			var out []string
			for d := a.FirstDnsServerAddress; d != nil; d = d.Next {
				if s := dnsServerString(&d.Address); s != "" {
					out = append(out, s)
				}
			}
			return out, nil
		}
		return nil, fmt.Errorf("no adapter with address %q found", ip)
	}
}

func adapterAddrHasIP(a *windows.IpAdapterAddresses, ip string) bool {
	for u := a.FirstUnicastAddress; u != nil; u = u.Next {
		if s := u.Address.IP(); s != nil && ip != "" && s.String() == ip {
			return true
		}
	}
	return false
}

// dnsServerString formats a DNS server address. A link-local IPv6 server
// keeps its zone (fe80::1%13): without it Windows sends the query out of
// any adapter it likes.
func dnsServerString(sa *windows.SocketAddress) string {
	ip := sa.IP()
	if ip == nil {
		return ""
	}
	s := ip.String()
	if ip.To4() == nil && ip.IsLinkLocalUnicast() {
		s += "%" + strconv.FormatUint(uint64((*windows.RawSockaddrInet6)(unsafe.Pointer(sa.Sockaddr)).Scope_id), 10)
	}
	return s
}

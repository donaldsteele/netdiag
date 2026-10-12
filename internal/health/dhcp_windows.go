//go:build windows

package health

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// readDHCP finds the adapter owning ip with GetAdaptersInfo (the data
// `ipconfig /all` prints) instead of running ipconfig.exe.
func readDHCP(_ context.Context, _ *Runner, _, ip string) (DHCPInfo, error) {
	ai, err := adaptersInfo()
	if err != nil {
		return DHCPInfo{}, err
	}
	for a := ai; a != nil; a = a.Next {
		if !adapterHasIP(a, ip) {
			continue
		}
		info := DHCPInfo{IP: ip, Enabled: a.DhcpEnabled != 0, APIPA: strings.HasPrefix(ip, "169.254.")}
		if info.Enabled {
			// No lease (APIPA) shows as 0.0.0.0 or 255.255.255.255.
			if s := windows.ByteSliceToString(a.DhcpServer.IpAddress.String[:]); s != "0.0.0.0" && s != "255.255.255.255" {
				info.Server = s
			}
			info.LeaseObtained = leaseTime(a.LeaseObtained)
			info.LeaseExpires = leaseTime(a.LeaseExpires)
		}
		return info, nil
	}
	return DHCPInfo{}, fmt.Errorf("no adapter with address %q found", ip)
}

// adaptersInfo returns the GetAdaptersInfo list, growing the buffer as asked.
func adaptersInfo() (*windows.IpAdapterInfo, error) {
	size := uint32(16 << 10)
	for {
		buf := make([]byte, size)
		ai := (*windows.IpAdapterInfo)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersInfo(ai, &size)
		if err == nil {
			return ai, nil
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return nil, fmt.Errorf("GetAdaptersInfo: %w", err)
		}
	}
}

func adapterHasIP(a *windows.IpAdapterInfo, ip string) bool {
	for s := &a.IpAddressList; s != nil; s = s.Next {
		if ip != "" && windows.ByteSliceToString(s.IpAddress.String[:]) == ip {
			return true
		}
	}
	return false
}

// leaseTime formats a lease time_t the way English ipconfig does.
func leaseTime(t int64) string {
	if t <= 0 {
		return ""
	}
	return time.Unix(t, 0).Format("Monday, January 2, 2006 3:04:05 PM")
}

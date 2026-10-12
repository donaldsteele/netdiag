//go:build windows

package arp

import (
	"cmp"
	"context"
	"errors"
	"net"
	"slices"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The neighbor (ARP) cache is read with GetIpNetTable2 instead of running
// arp.exe: no child process, and no English-only output to parse.
var procGetIpNetTable2 = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetIpNetTable2")

// mibIpNetRow2 is MIB_IPNET_ROW2 from netioapi.h (88 bytes).
type mibIpNetRow2 struct {
	Address               windows.RawSockaddrInet
	InterfaceIndex        uint32
	InterfaceLuid         uint64
	PhysicalAddress       [32]byte // IF_MAX_PHYS_ADDRESS_LENGTH
	PhysicalAddressLength uint32
	State                 uint32 // NL_NEIGHBOR_STATE
	Flags                 uint8  // IsRouter, IsUnreachable bits
	ReachabilityTime      uint32
}

// mibIpNetTable2 is MIB_IPNET_TABLE2.
type mibIpNetTable2 struct {
	NumEntries uint32
	Table      [1]mibIpNetRow2
}

// NL_NEIGHBOR_STATE values from nldef.h.
const (
	nlnsUnreachable = 0
	nlnsIncomplete  = 1
	nlnsPermanent   = 6
)

func readTable(context.Context) ([]Entry, error) {
	if err := procGetIpNetTable2.Find(); err != nil {
		return nil, err
	}
	var tab *mibIpNetTable2
	r, _, _ := procGetIpNetTable2.Call(uintptr(windows.AF_INET), uintptr(unsafe.Pointer(&tab)))
	if err := windows.Errno(r); r != 0 {
		if errors.Is(err, windows.ERROR_NOT_FOUND) {
			return nil, nil
		}
		return nil, err
	}
	rows := slices.Clone(unsafe.Slice(&tab.Table[0], tab.NumEntries))
	windows.FreeMibTable(unsafe.Pointer(tab))
	return entriesFromRows(rows, interfaceIPv4), nil
}

// entriesFromRows converts IPv4 neighbor rows to entries, grouped by adapter
// index like `arp -a`. ifaceIP maps an adapter index to its IPv4 address, or
// "" for an adapter that is down; like `arp -a`, rows on such adapters are
// dropped (a disconnected Wi-Fi adapter keeps stale rows). Unresolved rows
// (unreachable, incomplete) and multicast/broadcast rows are dropped too;
// permanent rows are typed "static", the rest "dynamic".
func entriesFromRows(rows []mibIpNetRow2, ifaceIP func(idx uint32) string) []Entry {
	slices.SortStableFunc(rows, func(a, b mibIpNetRow2) int {
		return cmp.Compare(a.InterfaceIndex, b.InterfaceIndex)
	})
	ifaces := make(map[uint32]string)
	var out []Entry
	for i := range rows {
		row := &rows[i]
		if row.State == nlnsUnreachable || row.State == nlnsIncomplete || row.PhysicalAddressLength != 6 {
			continue
		}
		ip, ok := ifaces[row.InterfaceIndex]
		if !ok {
			ip = ifaceIP(row.InterfaceIndex)
			ifaces[row.InterfaceIndex] = ip
		}
		if ip == "" {
			continue
		}
		sa := (*windows.RawSockaddrInet4)(unsafe.Pointer(&row.Address))
		n := len(out)
		out = appendEntry(out, net.IP(sa.Addr[:]).String(), net.HardwareAddr(row.PhysicalAddress[:6]).String())
		if len(out) == n {
			continue
		}
		out[n].Iface = ip
		out[n].Type = "dynamic"
		if row.State == nlnsPermanent {
			out[n].Type = "static"
		}
	}
	return out
}

// interfaceIPv4 returns the first IPv4 address of the adapter with index
// idx, or "" if the adapter is down or has none.
func interfaceIPv4(idx uint32) string {
	ifc, err := net.InterfaceByIndex(int(idx))
	if err != nil || ifc.Flags&net.FlagUp == 0 {
		return ""
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if ip4 := ipn.IP.To4(); ip4 != nil {
				return ip4.String()
			}
		}
	}
	return ""
}

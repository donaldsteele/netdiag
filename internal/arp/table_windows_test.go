//go:build windows

package arp

import (
	"context"
	"net"
	"reflect"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestMibIpNetRow2Layout(t *testing.T) {
	if got := unsafe.Sizeof(mibIpNetRow2{}); got != 88 {
		t.Fatalf("sizeof(MIB_IPNET_ROW2) = %d want 88", got)
	}
	if got := unsafe.Offsetof(mibIpNetTable2{}.Table); got != 8 {
		t.Fatalf("MIB_IPNET_TABLE2.Table offset = %d want 8", got)
	}
}

func netRow(idx uint32, ip, mac string, state uint32) mibIpNetRow2 {
	var r mibIpNetRow2
	sa := (*windows.RawSockaddrInet4)(unsafe.Pointer(&r.Address))
	sa.Family = windows.AF_INET
	copy(sa.Addr[:], net.ParseIP(ip).To4())
	hw, err := net.ParseMAC(mac)
	if err != nil {
		panic(err)
	}
	r.PhysicalAddressLength = uint32(copy(r.PhysicalAddress[:], hw))
	r.InterfaceIndex = idx
	r.State = state
	return r
}

func TestEntriesFromRows(t *testing.T) {
	const reachable, stale, delay = 5, 4, 3
	rows := []mibIpNetRow2{
		netRow(11, "10.0.0.9", "3c:22:fb:01:02:03", reachable),
		netRow(7, "192.168.1.1", "a4:2b:b0:12:34:56", stale),
		netRow(7, "192.168.1.77", "ff:ff:ff:ff:ff:ff", nlnsPermanent),
		netRow(7, "224.0.0.22", "01:00:5e:00:00:16", nlnsPermanent),
		netRow(11, "10.0.0.1", "00:00:00:00:00:00", nlnsUnreachable),
		netRow(11, "10.0.0.2", "00:50:56:aa:bb:01", nlnsIncomplete),
		netRow(7, "192.168.1.30", "00:50:56:aa:bb:02", nlnsPermanent),
		netRow(7, "192.168.1.20", "00:1b:a9:aa:bb:cc", delay),
		netRow(9, "192.168.1.66", "38:f7:cd:ca:0d:ca", stale), // adapter down
	}
	calls := map[uint32]int{}
	ifaceIP := func(idx uint32) string {
		calls[idx]++
		return map[uint32]string{7: "192.168.1.50", 11: "10.0.0.5"}[idx]
	}
	want := []Entry{
		{IP: "192.168.1.1", MAC: "A4:2B:B0:12:34:56", Iface: "192.168.1.50", Type: "dynamic"},
		{IP: "192.168.1.30", MAC: "00:50:56:AA:BB:02", Iface: "192.168.1.50", Type: "static"},
		{IP: "192.168.1.20", MAC: "00:1B:A9:AA:BB:CC", Iface: "192.168.1.50", Type: "dynamic"},
		{IP: "10.0.0.9", MAC: "3C:22:FB:01:02:03", Iface: "10.0.0.5", Type: "dynamic"},
	}
	if got := entriesFromRows(rows, ifaceIP); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if calls[7] != 1 || calls[9] != 1 || calls[11] != 1 {
		t.Fatalf("adapter lookups not cached: %v", calls)
	}
}

func TestReadTableLive(t *testing.T) {
	entries, err := readTable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, ok := NormalizeMAC(e.MAC); !ok || net.ParseIP(e.IP).To4() == nil {
			t.Errorf("bad entry %+v", e)
		}
		if e.Type != "dynamic" && e.Type != "static" {
			t.Errorf("bad type %+v", e)
		}
	}
}

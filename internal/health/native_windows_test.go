//go:build windows

package health

import (
	"context"
	"net"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWlanStructLayout(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"WLAN_INTERFACE_INFO", unsafe.Sizeof(wlanInterfaceInfo{}), 532},
		{"WLAN_INTERFACE_INFO_LIST.InterfaceInfo", unsafe.Offsetof(wlanInterfaceInfoList{}.InterfaceInfo), 8},
		{"WLAN_ASSOCIATION_ATTRIBUTES", unsafe.Sizeof(wlanAssociationAttributes{}), 68},
		{"WLAN_CONNECTION_ATTRIBUTES", unsafe.Sizeof(wlanConnectionAttributes{}), 604},
		{"WLAN_BSS_ENTRY", unsafe.Sizeof(wlanBssEntry{}), 360},
		{"WLAN_BSS_ENTRY.ulChCenterFrequency", unsafe.Offsetof(wlanBssEntry{}.ChCenterFrequency), 92},
		{"WLAN_BSS_LIST.wlanBssEntries", unsafe.Offsetof(wlanBssList{}.Entries), 8},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d want %d", c.name, c.got, c.want)
		}
	}
}

func TestWiFiFromAttrs(t *testing.T) {
	var attrs wlanConnectionAttributes
	attrs.State = wlanInterfaceStateConnected
	a := &attrs.Association
	a.Ssid.Length = uint32(copy(a.Ssid.SSID[:], "EventNet"))
	a.Bssid = [6]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66}
	a.PhyType = 10
	a.SignalQuality = 87
	a.TxRate = 780000
	w := wifiFromAttrs(&attrs)
	want := WiFiInfo{Connected: true, SSID: "EventNet", BSSID: "11:22:33:44:55:66", PHY: "802.11ax",
		SignalPct: 87, RSSI: -57, TxRateMbps: 780}
	if w != want {
		t.Fatalf("got %+v\nwant %+v", w, want)
	}
}

func TestBandFromFreqKHz(t *testing.T) {
	for f, want := range map[uint32]string{0: "", 2_437_000: "2.4 GHz", 5_180_000: "5 GHz", 5_885_000: "5 GHz", 5_955_000: "6 GHz"} {
		if got := bandFromFreqKHz(f); got != want {
			t.Errorf("bandFromFreqKHz(%d) = %q want %q", f, got, want)
		}
	}
}

func TestWlanError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{windows.ERROR_ACCESS_DENIED, "Location services"},
		{windows.ERROR_SERVICE_NOT_ACTIVE, "WLAN AutoConfig"},
		{windows.ERROR_INVALID_PARAMETER, "wlan: "},
	} {
		if got := wlanError(tc.err).Error(); !strings.Contains(got, tc.want) {
			t.Errorf("wlanError(%v) = %q, want it to contain %q", tc.err, got, tc.want)
		}
	}
}

// TestReadWiFiLive only checks the call is well formed: the result depends
// on this machine (no adapter, disconnected, location off, ...).
func TestReadWiFiLive(t *testing.T) {
	w, err := readWiFi(context.Background(), nil, "")
	t.Logf("%+v %v", w, err)
}

func TestReadDHCPLive(t *testing.T) {
	var ip string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
			ip = ipn.IP.To4().String()
			break
		}
	}
	if ip == "" {
		t.Skip("no IPv4 address")
	}
	info, err := readDHCP(context.Background(), nil, "", ip)
	if err != nil || info.IP != ip {
		t.Fatalf("%+v %v", info, err)
	}
	if _, err := readDHCP(context.Background(), nil, "", "203.0.113.250"); err == nil {
		t.Fatal("unknown ip must error")
	}
	t.Logf("%+v", info)
}

//go:build windows

package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Wi-Fi details come from the Native Wifi API (wlanapi.dll) instead of
// running `netsh wlan show interfaces`: no child process, and no English-only
// output to parse.
var (
	modWlanapi                = windows.NewLazySystemDLL("wlanapi.dll")
	procWlanOpenHandle        = modWlanapi.NewProc("WlanOpenHandle")
	procWlanCloseHandle       = modWlanapi.NewProc("WlanCloseHandle")
	procWlanEnumInterfaces    = modWlanapi.NewProc("WlanEnumInterfaces")
	procWlanQueryInterface    = modWlanapi.NewProc("WlanQueryInterface")
	procWlanGetNetworkBssList = modWlanapi.NewProc("WlanGetNetworkBssList")
	procWlanFreeMemory        = modWlanapi.NewProc("WlanFreeMemory")
)

const (
	wlanInterfaceStateConnected = 1 // WLAN_INTERFACE_STATE

	// WLAN_INTF_OPCODE values.
	wlanIntfOpcodeCurrentConn   = 7
	wlanIntfOpcodeChannelNumber = 8
	wlanIntfOpcodeRssi          = 0x10000102
)

// Structures from wlanapi.h / windot11.h.
type wlanInterfaceInfo struct {
	InterfaceGuid windows.GUID
	Description   [256]uint16
	State         uint32
}

type wlanInterfaceInfoList struct {
	NumberOfItems uint32
	Index         uint32
	InterfaceInfo [1]wlanInterfaceInfo
}

type dot11Ssid struct {
	Length uint32
	SSID   [32]byte
}

type wlanAssociationAttributes struct {
	Ssid          dot11Ssid
	BssType       uint32
	Bssid         [6]byte
	PhyType       uint32
	PhyIndex      uint32
	SignalQuality uint32 // 0-100
	RxRate        uint32 // kbps
	TxRate        uint32 // kbps
}

type wlanSecurityAttributes struct {
	SecurityEnabled uint32
	OneXEnabled     uint32
	AuthAlgorithm   uint32
	CipherAlgorithm uint32
}

type wlanConnectionAttributes struct {
	State          uint32
	ConnectionMode uint32
	ProfileName    [256]uint16
	Association    wlanAssociationAttributes
	Security       wlanSecurityAttributes
}

type wlanBssEntry struct {
	Ssid              dot11Ssid
	PhyID             uint32
	Bssid             [6]byte
	BssType           uint32
	PhyType           uint32
	Rssi              int32
	LinkQuality       uint32
	InRegDomain       uint8
	BeaconPeriod      uint16
	Timestamp         uint64
	HostTimestamp     uint64
	CapabilityInfo    uint16
	ChCenterFrequency uint32 // kHz
	RateSet           struct {
		Length uint32
		Rates  [126]uint16
	}
	IeOffset uint32
	IeSize   uint32
}

type wlanBssList struct {
	TotalSize     uint32
	NumberOfItems uint32
	Entries       [1]wlanBssEntry
}

// readWiFi reports the first connected wireless interface, as netsh does.
func readWiFi(context.Context, *Runner, string) (WiFiInfo, error) {
	if err := modWlanapi.Load(); err != nil {
		return WiFiInfo{}, errors.New("Wi-Fi service (WLAN AutoConfig) is not installed")
	}
	var negotiated uint32
	var h windows.Handle
	if err := wlanCall(procWlanOpenHandle, 2, 0, uintptr(unsafe.Pointer(&negotiated)), uintptr(unsafe.Pointer(&h))); err != nil {
		return WiFiInfo{}, wlanError(err)
	}
	defer procWlanCloseHandle.Call(uintptr(h), 0)

	var list *wlanInterfaceInfoList
	if err := wlanCall(procWlanEnumInterfaces, uintptr(h), 0, uintptr(unsafe.Pointer(&list))); err != nil {
		return WiFiInfo{}, wlanError(err)
	}
	defer wlanFree(unsafe.Pointer(list))
	infos := unsafe.Slice(&list.InterfaceInfo[0], list.NumberOfItems)
	for i := range infos {
		if infos[i].State == wlanInterfaceStateConnected {
			return readConnection(h, &infos[i].InterfaceGuid)
		}
	}
	return WiFiInfo{Connected: false}, nil
}

func readConnection(h windows.Handle, guid *windows.GUID) (WiFiInfo, error) {
	p, err := queryInterface(h, guid, wlanIntfOpcodeCurrentConn)
	if err != nil {
		return WiFiInfo{}, wlanError(err)
	}
	defer wlanFree(p)
	attrs := (*wlanConnectionAttributes)(p)
	if attrs.State != wlanInterfaceStateConnected {
		return WiFiInfo{Connected: false}, nil
	}
	w := wifiFromAttrs(attrs)
	if p, err := queryInterface(h, guid, wlanIntfOpcodeRssi); err == nil {
		if rssi := *(*int32)(p); rssi < 0 {
			w.RSSI = int(rssi)
		}
		wlanFree(p)
	}
	if p, err := queryInterface(h, guid, wlanIntfOpcodeChannelNumber); err == nil {
		w.Channel = int(*(*uint32)(p))
		wlanFree(p)
	}
	if w.Band = bandFromFreqKHz(bssFrequency(h, guid, attrs)); w.Band == "" {
		w.Band = bandFromChannel(w.Channel)
	}
	return w, nil
}

// wifiFromAttrs maps a connected interface's attributes. RSSI is estimated
// from the signal quality until the driver reports it.
func wifiFromAttrs(attrs *wlanConnectionAttributes) WiFiInfo {
	a := &attrs.Association
	w := WiFiInfo{
		Connected:  true,
		SSID:       string(a.Ssid.SSID[:min(a.Ssid.Length, uint32(len(a.Ssid.SSID)))]),
		BSSID:      net.HardwareAddr(a.Bssid[:]).String(),
		PHY:        phyName(a.PhyType),
		SignalPct:  int(min(a.SignalQuality, 100)),
		TxRateMbps: float64(a.TxRate) / 1000,
	}
	w.RSSI = w.SignalPct/2 - 100
	return w
}

// bssFrequency looks up the connected BSS's centre frequency (kHz) in the
// cached scan list; 0 if it is not listed.
func bssFrequency(h windows.Handle, guid *windows.GUID, attrs *wlanConnectionAttributes) uint32 {
	a := &attrs.Association
	var list *wlanBssList
	if wlanCall(procWlanGetNetworkBssList, uintptr(h), uintptr(unsafe.Pointer(guid)), uintptr(unsafe.Pointer(&a.Ssid)),
		uintptr(a.BssType), uintptr(attrs.Security.SecurityEnabled), 0, uintptr(unsafe.Pointer(&list))) != nil {
		return 0
	}
	defer wlanFree(unsafe.Pointer(list))
	entries := unsafe.Slice(&list.Entries[0], list.NumberOfItems)
	for i := range entries {
		if entries[i].Bssid == a.Bssid {
			return entries[i].ChCenterFrequency
		}
	}
	return 0
}

func queryInterface(h windows.Handle, guid *windows.GUID, op uint32) (unsafe.Pointer, error) {
	var size uint32
	var data unsafe.Pointer
	err := wlanCall(procWlanQueryInterface, uintptr(h), uintptr(unsafe.Pointer(guid)), uintptr(op), 0,
		uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&data)), 0)
	return data, err
}

func wlanCall(p *windows.LazyProc, args ...uintptr) error {
	if err := p.Find(); err != nil {
		return err
	}
	if r, _, _ := p.Call(args...); r != 0 {
		return windows.Errno(r)
	}
	return nil
}

func wlanFree(p unsafe.Pointer) { procWlanFreeMemory.Call(uintptr(p)) }

// wlanError explains the WLAN API errors users can fix.
func wlanError(err error) error {
	switch {
	case errors.Is(err, windows.ERROR_ACCESS_DENIED): // Windows 11 24H2+ without location consent
		return fmt.Errorf("%w: Settings > Privacy & security > Location, turn on Location services and Let desktop apps access your location", ErrLocationOff)
	case errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE):
		return errors.New("Wi-Fi service (WLAN AutoConfig) is not running")
	}
	return fmt.Errorf("wlan: %w", err)
}

// phyName maps DOT11_PHY_TYPE to the "Radio type" netsh shows.
func phyName(t uint32) string {
	switch t {
	case 4:
		return "802.11a"
	case 5:
		return "802.11b"
	case 6:
		return "802.11g"
	case 7:
		return "802.11n"
	case 8:
		return "802.11ac"
	case 9:
		return "802.11ad"
	case 10:
		return "802.11ax"
	case 11:
		return "802.11be"
	}
	return ""
}

// bandFromFreqKHz names the band of a channel centre frequency; "" if unknown.
func bandFromFreqKHz(f uint32) string {
	switch {
	case f == 0:
		return ""
	case f < 3_000_000:
		return "2.4 GHz"
	case f < 5_925_000:
		return "5 GHz"
	default:
		return "6 GHz"
	}
}

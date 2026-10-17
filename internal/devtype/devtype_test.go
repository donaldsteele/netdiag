package devtype

import (
	"strings"
	"testing"

	"netdiag/internal/model"
)

func mdns(host string, svcs ...string) *model.MDNSInfo {
	return &model.MDNSInfo{Hostname: host, Services: svcs}
}

func TestClassify(t *testing.T) {
	const gw = "192.168.0.1"
	tests := []struct {
		name   string
		h      model.Host
		want   Type
		reason string
	}{
		{"self beats all", model.Host{IP: gw, IsSelf: true, IsPrinter: true}, Self, "this computer"},
		{"gateway beats printer", model.Host{IP: gw, IsPrinter: true}, Gateway, "default gateway"},
		{"gateway TP-Link", model.Host{IP: gw, Vendor: "TP-LINK TECHNOLOGIES CO.,LTD."}, Gateway, "default gateway"},
		{"printer reason", model.Host{IP: "1", IsPrinter: true, PrinterReason: "port 9100"}, Printer, "printer: port 9100"},
		{"remote desktop only", model.Host{IP: "1", OpenPorts: []int{22, 3389}}, Computer, "remote desktop port 3389"},
		{"screen sharing only", model.Host{IP: "1", OpenPorts: []int{5900}}, Computer, "screen sharing port 5900"},
		{"web beats remote desktop", model.Host{IP: "1", OpenPorts: []int{80, 3389}}, Server, "open port 80"},
		{"ssh alone says nothing", model.Host{IP: "1", OpenPorts: []int{22}}, Unknown, "no identifying signals"},
		{"printer bare", model.Host{IP: "1", IsPrinter: true}, Printer, "printer"},
		{"printer beats mdns", model.Host{IP: "1", IsPrinter: true, MDNS: mdns("", "_airplay._tcp")}, Printer, ""},
		{"mdns ipp", model.Host{IP: "1", MDNS: mdns("", "_ipp._tcp")}, Printer, "mdns _ipp"},
		{"mdns pdl", model.Host{IP: "1", MDNS: mdns("", "_pdl-datastream._tcp")}, Printer, ""},
		{"mdns rtsp", model.Host{IP: "1", MDNS: mdns("", "_rtsp._tcp")}, Camera, ""},
		{"mdns onvif", model.Host{IP: "1", MDNS: mdns("", "_onvif._tcp")}, Camera, ""},
		{"mdns apple-mobdev2", model.Host{IP: "1", MDNS: mdns("", "_apple-mobdev2._tcp")}, Phone, ""},
		{"airplay+companion", model.Host{IP: "1", Vendor: "Apple, Inc.", MDNS: mdns("", "_airplay._tcp", "_companion-link._tcp")}, Phone, "mdns _companion-link"},
		{"printer over camera", model.Host{IP: "1", MDNS: mdns("", "_rtsp._tcp", "_ipp._tcp")}, Printer, ""},
		{"smb generic", model.Host{IP: "1", MDNS: mdns("", "_smb._tcp")}, Computer, "mdns _smb"},
		{"smb synology", model.Host{IP: "1", Vendor: "Synology Incorporated", MDNS: mdns("", "_smb._tcp")}, NAS, ""},
		{"afp nas hostname", model.Host{IP: "1", Hostname: "TNAS-1234", MDNS: mdns("", "_afpovertcp._tcp")}, NAS, ""},
		{"nfs wd vendor", model.Host{IP: "1", Vendor: "Western Digital Corp", MDNS: mdns("", "_nfs._tcp")}, NAS, ""},
		{"adisk txt model", model.Host{IP: "1", MDNS: &model.MDNSInfo{Services: []string{"_adisk._tcp"}, TXT: map[string]string{"model": "QNAP TS-453"}}}, NAS, ""},
		{"adisk printer model", model.Host{IP: "1", Printer: &model.PrinterInfo{Model: "Buffalo LS"}, MDNS: mdns("", "_adisk._tcp")}, NAS, ""},
		{"airplay tv", model.Host{IP: "1", MDNS: mdns("", "_airplay._tcp")}, TV, "mdns _airplay"},
		{"googlecast", model.Host{IP: "1", MDNS: mdns("", "_googlecast._tcp")}, TV, ""},
		{"raop", model.Host{IP: "1", MDNS: mdns("", "_raop._tcp")}, TV, ""},
		{"roku svc", model.Host{IP: "1", MDNS: mdns("", "_roku._tcp")}, TV, ""},
		{"spotify", model.Host{IP: "1", MDNS: mdns("", "_spotify-connect._tcp")}, TV, ""},
		{"Hisense airplay", model.Host{IP: "1", Vendor: "Hisense Visual", MDNS: mdns("", "_airplay._tcp")}, TV, ""},
		{"mac airplay hostname", model.Host{IP: "1", Vendor: "Apple, Inc.", Hostname: "Dons-MacBook-Pro.local", MDNS: mdns("", "_airplay._tcp")}, Computer, "hostname macbook"},
		{"hap", model.Host{IP: "1", MDNS: mdns("", "_hap._tcp")}, IoT, ""},
		{"homekit", model.Host{IP: "1", MDNS: mdns("", "_homekit._tcp")}, IoT, ""},
		{"matter udp", model.Host{IP: "1", MDNS: mdns("", "_matter._udp")}, IoT, ""},
		{"hue", model.Host{IP: "1", MDNS: mdns("", "_hue._tcp")}, IoT, ""},
		{"tv beats iot", model.Host{IP: "1", MDNS: mdns("", "_hap._tcp", "_googlecast._tcp")}, TV, ""},
		{"snmp switch", model.Host{IP: "1", Snmp: &model.SnmpInfo{SysDescr: "24-port Gigabit Switch"}}, Network, "snmp sysDescr switch"},
		{"snmp ap", model.Host{IP: "1", Snmp: &model.SnmpInfo{SysName: "office AP"}}, Network, ""},
		{"snmp access point", model.Host{IP: "1", Snmp: &model.SnmpInfo{SysDescr: "Wireless Access Point"}}, Network, ""},
		{"snmp unifi", model.Host{IP: "1", Snmp: &model.SnmpInfo{SysObjectID: "unifi"}}, Network, ""},
		{"snmp nas", model.Host{IP: "1", Snmp: &model.SnmpInfo{SysDescr: "Linux Synology DS218"}}, NAS, ""},
		{"vendor synology", model.Host{IP: "1", Vendor: "Synology Incorporated"}, NAS, "vendor synology"},
		{"vendor terramaster", model.Host{IP: "1", Vendor: "TerraMaster Inc"}, NAS, ""},
		{"vendor hikvision", model.Host{IP: "1", Vendor: "Hangzhou Hikvision"}, Camera, ""},
		{"vendor axis", model.Host{IP: "1", Vendor: "Axis Communications AB"}, Camera, ""},
		{"vendor roku", model.Host{IP: "1", Vendor: "Roku, Inc"}, TV, ""},
		{"vendor lg", model.Host{IP: "1", Vendor: "LG Electronics (Mobile)"}, TV, ""},
		{"vendor sonos", model.Host{IP: "1", Vendor: "Sonos, Inc."}, TV, ""},
		{"vendor tplink", model.Host{IP: "1", Vendor: "TP-LINK TECHNOLOGIES CO.,LTD."}, Network, "vendor tp-link"},
		{"vendor ubiquiti", model.Host{IP: "1", Vendor: "Ubiquiti Inc"}, Network, ""},
		{"vendor espressif", model.Host{IP: "1", Vendor: "Espressif Inc."}, IoT, ""},
		{"vendor philips", model.Host{IP: "1", Vendor: "Philips Lighting BV"}, IoT, ""},
		{"vendor ring", model.Host{IP: "1", Vendor: "Ring LLC"}, IoT, ""},
		{"vendor samsung phone", model.Host{IP: "1", Vendor: "Samsung Electronics", Hostname: "Galaxy-S21"}, Phone, "vendor Samsung"},
		{"vendor apple iphone", model.Host{IP: "1", Vendor: "Apple, Inc.", Hostname: "Dons-iPhone"}, Phone, ""},
		{"vendor hon hai only", model.Host{IP: "1", Vendor: "Hon Hai Precision Ind. Co.,Ltd."}, Unknown, ""},
		{"host ipad", model.Host{IP: "1", Hostname: "ipad"}, Phone, "hostname ipad"},
		{"host android", model.Host{IP: "1", Hostname: "android-1234"}, Phone, ""},
		{"host pixel mdns", model.Host{IP: "1", MDNS: mdns("Pixel-7.local")}, Phone, ""},
		{"host desktop", model.Host{IP: "1", Hostname: "DESKTOP-ABC123"}, Computer, "hostname desktop"},
		{"host laptop", model.Host{IP: "1", Hostname: "LAPTOP-XYZ"}, Computer, ""},
		{"host -pc", model.Host{IP: "1", Hostname: "JOHN-PC"}, Computer, ""},
		{"host imac", model.Host{IP: "1", MDNS: &model.MDNSInfo{Instance: "Kitchen iMac"}}, Computer, ""},
		{"host thinkpad", model.Host{IP: "1", Hostname: "thinkpad-x1"}, Computer, ""},
		{"host nas", model.Host{IP: "1", Hostname: "my-nas"}, NAS, ""},
		{"host diskstation", model.Host{IP: "1", Hostname: "DiskStation"}, NAS, ""},
		{"host tnas", model.Host{IP: "1", Hostname: "TNAS-ABCD"}, NAS, ""},
		{"host cam", model.Host{IP: "1", Hostname: "front-cam"}, Camera, ""},
		{"host ipcam", model.Host{IP: "1", Hostname: "ipcam-01"}, Camera, ""},
		{"host tv", model.Host{IP: "1", Hostname: "Living-Room-TV"}, TV, ""},
		{"host chromecast", model.Host{IP: "1", Hostname: "Chromecast"}, TV, ""},
		{"host appletv", model.Host{IP: "1", Hostname: "AppleTV"}, TV, ""},
		{"guard tvheadend", model.Host{IP: "1", Hostname: "tvheadend"}, Unknown, ""},
		{"guard camden", model.Host{IP: "1", Hostname: "camden"}, Unknown, ""},
		{"guard apple vs ap", model.Host{IP: "1", Snmp: &model.SnmpInfo{SysDescr: "Apple"}}, Unknown, ""},
		{"guard lone desktop", model.Host{IP: "1", Hostname: "desktop"}, Unknown, ""},
		{"web port 80", model.Host{IP: "1", OpenPorts: []int{22, 80}}, Server, "open port 80"},
		{"web entry 8443", model.Host{IP: "1", Web: []model.WebService{{Port: 8443}}}, Server, "open port 8443"},
		{"web 443 port", model.Host{IP: "1", OpenPorts: []int{443}}, Server, ""},
		{"arp only", model.Host{IP: "1", MAC: "aa:bb:cc:dd:ee:ff", Vendor: "Unknown"}, Unknown, "no identifying signals"},
		{"mdns unknown svc", model.Host{IP: "1", MDNS: mdns("", "_foo._tcp")}, Unknown, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, why := Classify(tc.h, gw)
			if got != tc.want {
				t.Fatalf("type = %q (%s), want %q", got, why, tc.want)
			}
			if why == "" || !strings.Contains(why, tc.reason) {
				t.Fatalf("reason = %q, want contains %q", why, tc.reason)
			}
			g2, w2 := Classify(tc.h, gw)
			if g2 != got || w2 != why {
				t.Fatal("not idempotent")
			}
		})
	}
}

func TestNoGateway(t *testing.T) {
	if got, _ := Classify(model.Host{}, ""); got != Unknown {
		t.Fatalf("got %q", got)
	}
}

func TestMdnsName(t *testing.T) {
	if mdnsName("_IPP._tcp.local") != "_ipp" || mdnsName("_matter._udp") != "_matter" {
		t.Fatal("mdnsName")
	}
}

func TestLabel(t *testing.T) {
	want := map[Type]string{
		Gateway: "Gateway", Printer: "Printer", Computer: "Computer", Phone: "Phone/tablet",
		TV: "TV/streamer", NAS: "NAS/storage", Camera: "Camera", Network: "Network gear",
		IoT: "Smart home/IoT", Server: "Server/web device", Self: "This computer", Unknown: "Unknown",
	}
	if len(All()) != 12 {
		t.Fatal("All")
	}
	for _, ty := range All() {
		if Label(ty) != want[ty] {
			t.Errorf("Label(%q)=%q", ty, Label(ty))
		}
	}
	if Label("bogus") != "Unknown" {
		t.Fatal("bogus")
	}
}

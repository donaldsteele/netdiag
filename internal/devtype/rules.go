package devtype

import "regexp"

// mdnsGroup maps mDNS service names (first label, e.g. "_ipp") to a type.
type mdnsGroup struct {
	t        Type
	services []string
}

// mDNS groups are evaluated in a fixed order by Classify:
// printer > camera > phone > storage (nas/computer) > Mac model > tv >
// companion > iot.
//
// _companion-link is a weak phone signal: every Apple device that runs
// Companion (Macs, Apple TVs and HomePods as well as iPhones and iPads)
// announces it, so file sharing, a Mac model, the TV group and a computer
// hostname all win over it. _apple-mobdev2 (iPhone and iPad Wi-Fi sync)
// stays a strong phone signal.
var (
	mdnsPrinter   = mdnsGroup{Printer, []string{"_ipp", "_ipps", "_printer", "_pdl-datastream"}}
	mdnsCamera    = mdnsGroup{Camera, []string{"_rtsp", "_onvif"}}
	mdnsPhone     = mdnsGroup{Phone, []string{"_apple-mobdev2"}}
	mdnsTV        = mdnsGroup{TV, []string{"_airplay", "_googlecast", "_raop", "_roku", "_spotify-connect"}}
	mdnsCompanion = mdnsGroup{Phone, []string{"_companion-link"}}
	mdnsIoT       = mdnsGroup{IoT, []string{"_hap", "_homekit", "_matter", "_hue"}}
)

var storageServices = []string{"_smb", "_afpovertcp", "_afpovertls", "_nfs", "_adisk"}

// macModelKeys are the TXT keys in which Apple devices give their model
// identifier: AirPlay's "model", RAOP's "am" and Companion's "rpMd" (the
// mDNS parser lower-cases keys).
var macModelKeys = []string{"model", "am", "rpmd"}

// macModelID matches the model identifier of a Mac: "Mac14,3" (Mac mini,
// Mac Studio), "Macmini9,1", "MacPro7,1", "MacBookPro18,3", "iMac21,1",
// "iMacPro1,1" and "VirtualMac2,1". Apple TVs ("AppleTV11,1"), HomePods
// ("AudioAccessory5,1"), iPhones and iPads never match.
var macModelID = regexp.MustCompile(`^(?:Virtual)?i?Mac[A-Za-z]*[0-9]+,[0-9]+$`)

// MDNSServices returns every DNS-SD service type the rules key on, as
// "_name._tcp". The mDNS browser must ask for each of them
// (mdns.DefaultServices), or the rule can never fire.
func MDNSServices() []string {
	var out []string
	for _, g := range []mdnsGroup{mdnsPrinter, mdnsCamera, mdnsPhone, mdnsTV, mdnsCompanion, mdnsIoT, {services: storageServices}} {
		for _, s := range g.services {
			out = append(out, s+"._tcp")
		}
	}
	return out
}

// nasKeywords are token sequences indicating NAS vendors/models.
var nasKeywords = []string{
	"synology", "qnap", "terramaster", "asustor", "wd", "western digital",
	"buffalo", "tnas", "diskstation",
}

var snmpNetworkKeywords = []string{
	"switch", "router", "access point", "ap", "mesh", "deco", "unifi", "edgeswitch",
}

var vendorRules = []struct {
	t        Type
	keywords []string
}{
	{NAS, []string{"synology", "qnap", "terramaster", "asustor"}},
	{Camera, []string{"hikvision", "dahua", "axis", "reolink", "amcrest"}},
	{TV, []string{"roku", "hisense", "tcl", "vizio", "sonos", "lg electronics"}},
	{Network, []string{"ubiquiti", "tp-link", "netgear", "cisco", "aruba", "mikrotik", "ruckus", "juniper"}},
	{IoT, []string{"espressif", "tuya", "shelly", "philips lighting", "signify", "nest", "ring", "ecobee"}},
}

// phoneVendors yield phone when combined with a phone hostname token.
var phoneVendors = []string{"samsung", "apple", "google"}

var phoneHostKeywords = []string{"iphone", "ipad", "android", "galaxy", "pixel"}

var computerHostKeywords = []string{"macbook", "imac", "thinkpad", "desktop-", "laptop-", "-pc"}

// hostRules are evaluated in order.
var hostRules = []struct {
	t        Type
	keywords []string
}{
	{Phone, phoneHostKeywords},
	{Computer, computerHostKeywords},
	{NAS, []string{"nas", "tnas", "diskstation"}},
	{Camera, []string{"cam", "ipcam"}},
	{TV, []string{"tv", "roku", "chromecast", "firetv", "appletv"}},
}

var webPorts = []int{80, 443, 8080, 8443}

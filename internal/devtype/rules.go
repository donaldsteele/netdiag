package devtype

// mdnsGroup maps mDNS service names (first label, e.g. "_ipp") to a type.
type mdnsGroup struct {
	t        Type
	services []string
}

// mDNS groups are evaluated in a fixed order by Classify:
// printer > camera > phone > storage (nas/computer) > tv > iot.
var (
	mdnsPrinter = mdnsGroup{Printer, []string{"_ipp", "_ipps", "_printer", "_pdl-datastream"}}
	mdnsCamera  = mdnsGroup{Camera, []string{"_rtsp", "_onvif"}}
	mdnsPhone   = mdnsGroup{Phone, []string{"_companion-link", "_apple-mobdev2"}}
	mdnsTV      = mdnsGroup{TV, []string{"_airplay", "_googlecast", "_raop", "_roku", "_spotify-connect"}}
	mdnsIoT     = mdnsGroup{IoT, []string{"_hap", "_homekit", "_matter", "_hue"}}
)

var storageServices = []string{"_smb", "_afpovertcp", "_afpovertls", "_nfs", "_adisk"}

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

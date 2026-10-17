package mdns

import (
	"encoding/hex"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

var update = flag.Bool("update", false, "regenerate testdata/mdns fixtures")

const fixtureDir = "../../testdata/mdns"

func mustName(s string) dnsmessage.Name { return dnsmessage.MustNewName(s) }

func rh(name string, typ dnsmessage.Type) dnsmessage.ResourceHeader {
	return dnsmessage.ResourceHeader{Name: mustName(name), Type: typ, Class: dnsmessage.ClassINET, TTL: 10}
}

// fixtureBuilders produce real-shaped legacy-unicast responses (question
// echoed, name compression, records split between answer and additional
// sections as mDNSResponder / printer stacks do).
var fixtureBuilders = map[string]func() ([]byte, error){
	"brother_ipp.hex": func() ([]byte, error) {
		const inst = "Brother HL-L2350DW series._ipp._tcp.local."
		const pdl = "Brother HL-L2350DW series._pdl-datastream._tcp.local."
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 0x1234, Response: true, Authoritative: true})
		b.EnableCompression()
		_ = b.StartQuestions()
		_ = b.Question(dnsmessage.Question{Name: mustName("_ipp._tcp.local."), Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET})
		_ = b.StartAnswers()
		_ = b.PTRResource(rh("_ipp._tcp.local.", dnsmessage.TypePTR), dnsmessage.PTRResource{PTR: mustName(inst)})
		_ = b.PTRResource(rh("_pdl-datastream._tcp.local.", dnsmessage.TypePTR), dnsmessage.PTRResource{PTR: mustName(pdl)})
		_ = b.StartAdditionals()
		_ = b.SRVResource(rh(inst, dnsmessage.TypeSRV), dnsmessage.SRVResource{Port: 631, Target: mustName("BRN3C2AF4.local.")})
		_ = b.SRVResource(rh(pdl, dnsmessage.TypeSRV), dnsmessage.SRVResource{Port: 9100, Target: mustName("BRN3C2AF4.local.")})
		_ = b.TXTResource(rh(inst, dnsmessage.TypeTXT), dnsmessage.TXTResource{TXT: []string{
			"txtvers=1", "qtotal=1", "rp=ipp/print", "ty=Brother HL-L2350DW series",
			"product=(Brother HL-L2350DW series)", "adminurl=http://BRN3C2AF4.local./",
			"pdl=application/octet-stream,image/urf,image/pwg-raster", "Color=F", "Duplex=T", "note=",
		}})
		_ = b.AResource(rh("BRN3C2AF4.local.", dnsmessage.TypeA), dnsmessage.AResource{A: [4]byte{192, 168, 1, 50}})
		_ = b.AAAAResource(rh("BRN3C2AF4.local.", dnsmessage.TypeAAAA), dnsmessage.AAAAResource{AAAA: [16]byte{0xfe, 0x80, 15: 1}})
		return b.Finish()
	},
	"nas_http.hex": func() ([]byte, error) {
		const inst = "Office NAS._http._tcp.local."
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 0x2222, Response: true, Authoritative: true})
		b.EnableCompression()
		_ = b.StartAnswers()
		_ = b.PTRResource(rh("_http._tcp.local.", dnsmessage.TypePTR), dnsmessage.PTRResource{PTR: mustName(inst)})
		_ = b.SRVResource(rh(inst, dnsmessage.TypeSRV), dnsmessage.SRVResource{Port: 5000, Target: mustName("nas.local.")})
		return b.Finish()
	},
	"nas_a.hex": func() ([]byte, error) {
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 0x2223, Response: true, Authoritative: true})
		_ = b.StartAnswers()
		_ = b.AResource(rh("nas.local.", dnsmessage.TypeA), dnsmessage.AResource{A: [4]byte{192, 168, 1, 20}})
		return b.Finish()
	},
	// A bubbles team chat server that also announces a web page. Both
	// services have a "v" TXT key with different meanings.
	"bubbles.hex": func() ([]byte, error) {
		const chat = "Team chat._bubbles._tcp.local."
		const web = "Team chat web._http._tcp.local."
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 0x4444, Response: true, Authoritative: true})
		b.EnableCompression()
		_ = b.StartAnswers()
		_ = b.PTRResource(rh("_bubbles._tcp.local.", dnsmessage.TypePTR), dnsmessage.PTRResource{PTR: mustName(chat)})
		_ = b.PTRResource(rh("_http._tcp.local.", dnsmessage.TypePTR), dnsmessage.PTRResource{PTR: mustName(web)})
		_ = b.StartAdditionals()
		_ = b.SRVResource(rh(chat, dnsmessage.TypeSRV), dnsmessage.SRVResource{Port: 28225, Target: mustName("chat.local.")})
		_ = b.SRVResource(rh(web, dnsmessage.TypeSRV), dnsmessage.SRVResource{Port: 8080, Target: mustName("chat.local.")})
		_ = b.TXTResource(rh(chat, dnsmessage.TypeTXT), dnsmessage.TXTResource{TXT: []string{
			"fp=3f9a1c2e7b4d5a60", "onboard=1", "v=0.2.0",
		}})
		_ = b.TXTResource(rh(web, dnsmessage.TypeTXT), dnsmessage.TXTResource{TXT: []string{"v=2", "path=/admin"}})
		_ = b.AResource(rh("chat.local.", dnsmessage.TypeA), dnsmessage.AResource{A: [4]byte{192, 168, 1, 40}})
		return b.Finish()
	},
	// A phone that announces only _companion-link._tcp, under a host name
	// that says nothing about phones.
	"companion_link.hex": func() ([]byte, error) {
		const inst = "Living room._companion-link._tcp.local."
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 0x5555, Response: true, Authoritative: true})
		b.EnableCompression()
		_ = b.StartAnswers()
		_ = b.PTRResource(rh("_companion-link._tcp.local.", dnsmessage.TypePTR), dnsmessage.PTRResource{PTR: mustName(inst)})
		_ = b.StartAdditionals()
		_ = b.SRVResource(rh(inst, dnsmessage.TypeSRV), dnsmessage.SRVResource{Port: 49152, Target: mustName("host-33.local.")})
		_ = b.TXTResource(rh(inst, dnsmessage.TypeTXT), dnsmessage.TXTResource{TXT: []string{"rpVr=360.4", "rpFl=0x20000"}})
		_ = b.AResource(rh("host-33.local.", dnsmessage.TypeA), dnsmessage.AResource{A: [4]byte{192, 168, 1, 33}})
		return b.Finish()
	},
	// An iPhone with Wi-Fi sync on. _apple-mobdev2 names its instance with
	// the MAC and link-local IPv6 address; _companion-link uses the phone's
	// name.
	"iphone.hex": func() ([]byte, error) {
		const sync = "70:3e:ac:12:34:56@fe80::723e:acff:fe12:3456-supportsRP-24._apple-mobdev2._tcp.local."
		const comp = "Don's iPhone._companion-link._tcp.local."
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 0x6666, Response: true, Authoritative: true})
		b.EnableCompression()
		_ = b.StartAnswers()
		_ = b.PTRResource(rh("_apple-mobdev2._tcp.local.", dnsmessage.TypePTR), dnsmessage.PTRResource{PTR: mustName(sync)})
		_ = b.PTRResource(rh("_companion-link._tcp.local.", dnsmessage.TypePTR), dnsmessage.PTRResource{PTR: mustName(comp)})
		_ = b.StartAdditionals()
		_ = b.SRVResource(rh(sync, dnsmessage.TypeSRV), dnsmessage.SRVResource{Port: 32498, Target: mustName("Dons-iPhone.local.")})
		_ = b.SRVResource(rh(comp, dnsmessage.TypeSRV), dnsmessage.SRVResource{Port: 49153, Target: mustName("Dons-iPhone.local.")})
		_ = b.TXTResource(rh(comp, dnsmessage.TypeTXT), dnsmessage.TXTResource{TXT: []string{"rpVr=360.4", "rpMd=iPhone15,2"}})
		_ = b.AResource(rh("Dons-iPhone.local.", dnsmessage.TypeA), dnsmessage.AResource{A: [4]byte{192, 168, 1, 34}})
		return b.Finish()
	},
	"cast_ptr.hex": func() ([]byte, error) {
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 0x3333, Response: true, Authoritative: true})
		_ = b.StartAnswers()
		_ = b.PTRResource(rh("_googlecast._tcp.local.", dnsmessage.TypePTR),
			dnsmessage.PTRResource{PTR: mustName("Living Room TV._googlecast._tcp.local.")})
		return b.Finish()
	},
}

func TestUpdateFixtures(t *testing.T) {
	if !*update {
		t.Skip("run with -update to regenerate")
	}
	for name, build := range fixtureBuilders {
		b, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var sb strings.Builder
		h := hex.EncodeToString(b)
		for i := 0; i < len(h); i += 64 {
			sb.WriteString(h[i:min(i+64, len(h))])
			sb.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(fixtureDir, name), []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readHex(tb testing.TB, name string) []byte {
	tb.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		tb.Fatal(err)
	}
	b, err := hex.DecodeString(strings.Join(strings.Fields(string(raw)), ""))
	if err != nil {
		tb.Fatalf("%s: %v", name, err)
	}
	return b
}

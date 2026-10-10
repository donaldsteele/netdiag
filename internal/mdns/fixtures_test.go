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

// Package mdns is a minimal mDNS/DNS-SD browser. It sends legacy-unicast
// queries (RFC 6762 section 6.7) from an ephemeral UDP port to
// 224.0.0.251:5353 and never binds port 5353, so it coexists with the OS
// responders (macOS mDNSResponder, Windows mDNS). Responses are aggregated per
// IPv4 address into model.MDNSInfo.
package mdns

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// DefaultWindow is the total browse window per scan (Locked Decision 3).
const DefaultWindow = 3 * time.Second

// MaxRounds is the maximum number of query rounds per Browse call.
const MaxRounds = 2

// maxFollowUps bounds round-2 SRV/TXT/A follow-up questions.
const maxFollowUps = 16

// maxQuerySize keeps a query inside one unfragmented datagram.
const maxQuerySize = 1400

// qu is the "unicast response" bit in the question class (RFC 6762 5.4).
const qu = 1 << 15

// DefaultServices are browsed when Browse is given no services. They cover
// every service the device type rules key on (devtype.MDNSServices) and
// _bubbles._tcp, the team chat server.
var DefaultServices = []string{
	"_ipp._tcp", "_ipps._tcp", "_printer._tcp", "_pdl-datastream._tcp",
	"_http._tcp", "_https._tcp", "_device-info._tcp", "_workstation._tcp",
	"_smb._tcp", "_airplay._tcp", "_raop._tcp", "_googlecast._tcp",
	"_scanner._tcp", "_uscan._tcp",
	// Cameras and phones.
	"_rtsp._tcp", "_onvif._tcp", "_companion-link._tcp", "_apple-mobdev2._tcp",
	// Smart home.
	"_hap._tcp", "_homekit._tcp", "_matter._tcp", "_hue._tcp",
	// TVs and speakers.
	"_roku._tcp", "_spotify-connect._tcp",
	// File sharing and storage.
	"_afpovertcp._tcp", "_afpovertls._tcp", "_nfs._tcp", "_adisk._tcp",
	// Team chat (bubbles).
	"_bubbles._tcp",
}

// Group is the IPv4 mDNS multicast destination.
var Group = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// Transport is the datagram socket used by Browse. *net.UDPConn satisfies
// it; tests replay fixture packets through a fake.
type Transport interface {
	WriteTo(p []byte, addr net.Addr) (int, error)
	ReadFrom(p []byte) (int, net.Addr, error)
	SetReadDeadline(t time.Time) error
	Close() error
}

// DialFunc opens a Transport that sends on the named interface ("" means the
// OS default route; an IPv4 address string selects the interface owning it).
type DialFunc func(ifaceName string) (Transport, error)

// Browser runs mDNS browses. The zero value uses the real UDP transport and
// MaxRounds rounds.
type Browser struct {
	Dial   DialFunc // nil: DialUDP
	Rounds int      // query rounds, 1..MaxRounds; <=0 means MaxRounds (live cycles use 1)
	Dest   net.Addr // nil: Group
}

// Browse runs one browse with the default Browser (2 rounds).
func Browse(ctx context.Context, ifaceName string, services []string, window time.Duration) ([]Record, error) {
	var b Browser
	return b.Browse(ctx, ifaceName, services, window)
}

// Browse queries services (DNS-SD types like "_ipp._tcp"; nil means
// DefaultServices) on ifaceName for at most window (<=0 means DefaultWindow)
// and returns one Record per responding IPv4 address, sorted by IP.
//
// Round 1 sends QU PTR questions at t=0. Round 2 (at window/3) repeats them
// as both QU and QM and adds SRV/TXT/A follow-ups for incompletely resolved
// instances. An empty result with a nil error means "no responses". An error
// is returned only when the socket cannot be opened or the first query
// cannot be sent. Cancelling ctx ends the browse early with what was
// collected so far.
func (b *Browser) Browse(ctx context.Context, ifaceName string, services []string, window time.Duration) ([]Record, error) {
	if window <= 0 {
		window = DefaultWindow
	}
	if len(services) == 0 {
		services = DefaultServices
	}
	rounds := b.Rounds
	if rounds <= 0 || rounds > MaxRounds {
		rounds = MaxRounds
	}
	dial := b.Dial
	if dial == nil {
		dial = DialUDP
	}
	dest := b.Dest
	if dest == nil {
		dest = Group
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	t, err := dial(ifaceName)
	if err != nil {
		return nil, fmt.Errorf("mdns: open socket: %w", err)
	}
	var once sync.Once
	closeT := func() { once.Do(func() { _ = t.Close() }) }
	defer closeT()
	stop := context.AfterFunc(ctx, closeT)
	defer stop()

	start := time.Now()
	end := start.Add(window)
	if dl, ok := ctx.Deadline(); ok && dl.Before(end) {
		end = dl
	}
	round2 := start.Add(window / 3)

	fqdn := make([]string, 0, len(services))
	for _, s := range services {
		if st := ServiceType(s); st != "" {
			fqdn = append(fqdn, st+".local.")
		}
	}
	col := newCollector()

	q, err := buildQuery(uint16(rand.Uint32()), fqdn, nil, nil, false)
	if err != nil {
		return nil, err
	}
	if _, err := t.WriteTo(q, dest); err != nil {
		return nil, fmt.Errorf("mdns: send query: %w", err)
	}
	sent := 1

	buf := make([]byte, 9000)
	readErrs := 0
	for ctx.Err() == nil {
		now := time.Now()
		if !now.Before(end) {
			break
		}
		dl := end
		if sent < rounds && round2.Before(dl) {
			dl = round2
		}
		if !now.Before(dl) {
			srv, a := col.pending(maxFollowUps)
			if q, err := buildQuery(uint16(rand.Uint32()), fqdn, srv, a, true); err == nil {
				_, _ = t.WriteTo(q, dest) // best effort
			}
			sent++
			continue
		}
		_ = t.SetReadDeadline(dl)
		n, addr, err := t.ReadFrom(buf)
		if err != nil {
			if isTimeout(err) {
				continue
			}
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				break
			}
			if readErrs++; readErrs > 16 {
				break
			}
			continue
		}
		ans, _ := Parse(buf[:n]) // partial answers are still useful
		col.add(ans, srcAddr(addr))
	}
	return col.records(), nil
}

func isTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func srcAddr(a net.Addr) netip.Addr {
	switch v := a.(type) {
	case *net.UDPAddr:
		if ip, ok := netip.AddrFromSlice(v.IP); ok {
			return ip.Unmap()
		}
	case interface{ AddrPort() netip.AddrPort }:
		return v.AddrPort().Addr().Unmap()
	}
	return netip.Addr{}
}

// buildQuery builds one query. PTR questions for services are always QU;
// when withQM is set they are repeated without the QU bit. srv names get
// SRV+TXT questions and a names get A questions (QU). Follow-ups are dropped
// if the packet would exceed maxQuerySize.
func buildQuery(id uint16, services, srv, a []string, withQM bool) ([]byte, error) {
	type question struct {
		name string
		typ  dnsmessage.Type
		cls  dnsmessage.Class
	}
	var qs []question
	for _, s := range services {
		qs = append(qs, question{s, dnsmessage.TypePTR, dnsmessage.ClassINET | qu})
	}
	if withQM {
		for _, s := range services {
			qs = append(qs, question{s, dnsmessage.TypePTR, dnsmessage.ClassINET})
		}
	}
	base := len(qs)
	for _, n := range srv {
		qs = append(qs, question{n, dnsmessage.TypeSRV, dnsmessage.ClassINET | qu},
			question{n, dnsmessage.TypeTXT, dnsmessage.ClassINET | qu})
	}
	for _, n := range a {
		qs = append(qs, question{n, dnsmessage.TypeA, dnsmessage.ClassINET | qu})
	}
	for {
		bld := dnsmessage.NewBuilder(make([]byte, 0, 512), dnsmessage.Header{ID: id})
		bld.EnableCompression()
		if err := bld.StartQuestions(); err != nil {
			return nil, err
		}
		for _, q := range qs {
			name, err := dnsmessage.NewName(q.name)
			if err != nil {
				continue // over-long instance name; skip it
			}
			if err := bld.Question(dnsmessage.Question{Name: name, Type: q.typ, Class: q.cls}); err != nil {
				continue
			}
		}
		msg, err := bld.Finish()
		if err != nil {
			return nil, err
		}
		if len(msg) <= maxQuerySize || len(qs) <= base {
			return msg, nil
		}
		qs = qs[:len(qs)-1]
	}
}

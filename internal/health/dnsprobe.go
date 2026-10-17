package health

import (
	"bufio"
	"context"
	"io"
	"math/rand/v2"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// dnsProbeTimeout bounds the question sent straight to the DNS servers.
const dnsProbeTimeout = 2 * time.Second

// dnsAnswerGrace is how long dnsReplies keeps listening after a first reply
// for the answer to the name itself, and how long localDNSAnswer waits for
// a better reply from another server.
const dnsAnswerGrace = 300 * time.Millisecond

// dnsResult is what a DNS server did with the questions, from least to most
// informative.
type dnsResult int

const (
	dnsNoReply    dnsResult = iota // no reply before the deadline
	dnsReplied                     // some reply, such as a server failure or a refusal
	dnsNoSuchName                  // the server says the name that failed does not exist
	dnsResolved                    // an address for the name that failed
)

// localDNSAnswer asks this computer's DNS servers directly, all at once,
// whether they answer at all. It returns the server with the most
// informative reply and what it was, or "" when none replied; asked lists
// the servers it asked, and err says why it could not ask any. The
// questions are the name that just failed and, for servers on the local
// network only, a reverse lookup of the gateway, which a local DNS server
// can answer without the internet.
func (r *Runner) localDNSAnswer(ctx context.Context, in Input, host string) (server string, reply dnsResult, asked []string, err error) {
	servers, err := r.dnsServers(ctx, in.Iface, in.IfaceIP)
	if err != nil {
		return "", dnsNoReply, nil, err
	}
	a, err := dnsmessage.NewName(strings.TrimSuffix(host, ".") + ".")
	if err != nil {
		return "", dnsNoReply, nil, err
	}
	nameQ := dnsmessage.Question{Name: a, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}
	// NewName checks only the total length; a bad label fails when packed.
	if _, err := (&dnsmessage.Message{Questions: []dnsmessage.Question{nameQ}}).Pack(); err != nil {
		return "", dnsNoReply, nil, err
	}
	var ptrQ *dnsmessage.Question
	if rev := reverseName(in.Gateway); rev != "" {
		ptrQ = &dnsmessage.Question{Name: dnsmessage.MustNewName(rev), Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}
	}
	for _, s := range servers {
		if usableDNSServer(s) && !slices.Contains(asked, s) {
			asked = append(asked, s)
		}
	}
	if len(asked) == 0 {
		return "", dnsNoReply, nil, nil
	}
	pctx, cancel := context.WithTimeout(ctx, dnsProbeTimeout)
	defer cancel()
	type res struct {
		server string
		reply  dnsResult
	}
	results := make(chan res, len(asked))
	var wg sync.WaitGroup
	for _, s := range asked {
		qs := []dnsmessage.Question{nameQ}
		if ptrQ != nil && localDNSServer(s) {
			qs = append(qs, *ptrQ)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rep := r.askDNS(pctx, s, qs); rep != dnsNoReply {
				results <- res{s, rep}
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	var best res
	var grace <-chan time.Time
	take := func(got res) {
		if got.reply > best.reply {
			best = got
		}
		switch {
		case best.reply >= dnsNoSuchName:
			cancel() // nothing better to wait for
		case grace == nil:
			grace = time.After(dnsAnswerGrace) // give the others a moment
		}
	}
	for {
		select {
		case got := <-results:
			take(got)
		case <-grace:
			cancel()
		case <-done:
			for len(results) > 0 {
				take(<-results)
			}
			return best.server, best.reply, asked, nil
		}
	}
}

// dnsReplies sends qs to the DNS server at server (an IP, or ip:port) over
// UDP and reports how it replied before ctx ends. Any reply with a matching
// ID counts, whatever its rcode; after the first one it waits a little for
// the answer to the A question.
func dnsReplies(ctx context.Context, server string, qs []dnsmessage.Question) dnsResult {
	addr := server
	if _, _, err := net.SplitHostPort(server); err != nil {
		addr = net.JoinHostPort(server, "53")
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return dnsNoReply
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { c.SetDeadline(time.Now()) })
	defer stop()
	ids := map[uint16]dnsmessage.Type{}
	for _, q := range qs {
		id := uint16(rand.Uint32())
		for _, dup := ids[id]; dup; _, dup = ids[id] {
			id = uint16(rand.Uint32())
		}
		m := dnsmessage.Message{Header: dnsmessage.Header{ID: id, RecursionDesired: true}, Questions: []dnsmessage.Question{q}}
		b, err := m.Pack()
		if err != nil {
			continue
		}
		if _, err := c.Write(b); err != nil {
			return dnsNoReply
		}
		ids[id] = q.Type
	}
	got := dnsNoReply
	buf := make([]byte, 4096)
	for len(ids) > 0 {
		n, err := c.Read(buf)
		if err != nil {
			// A timeout or the grace period ending. On macOS also "port
			// unreachable" from a host with no DNS server; Go turns those
			// reports off on Windows, which then waits for the timeout.
			return got
		}
		var p dnsmessage.Parser
		h, err := p.Start(buf[:n])
		qt, ok := ids[h.ID]
		if err != nil || !h.Response || !ok {
			continue
		}
		delete(ids, h.ID)
		if qt == dnsmessage.TypeA {
			if h.RCode == dnsmessage.RCodeSuccess && hasA(&p) {
				return dnsResolved
			}
			if h.RCode == dnsmessage.RCodeNameError {
				return dnsNoSuchName
			}
		}
		if got == dnsNoReply {
			got = dnsReplied
			g := time.Now().Add(dnsAnswerGrace)
			if dl, ok := ctx.Deadline(); ok && dl.Before(g) {
				g = dl
			}
			c.SetReadDeadline(g)
			if ctx.Err() != nil {
				return got
			}
		}
	}
	return got
}

// hasA reports whether the answer section holds an A record.
func hasA(p *dnsmessage.Parser) bool {
	if p.SkipAllQuestions() != nil {
		return false
	}
	for {
		h, err := p.AnswerHeader()
		if err != nil {
			return false
		}
		if h.Type == dnsmessage.TypeA {
			return true
		}
		if p.SkipAnswer() != nil {
			return false
		}
	}
}

// localDNSServer reports whether a DNS server is on the local network (or
// this computer): only those are asked about the gateway's address.
func localDNSServer(s string) bool {
	host := s
	if h, _, err := net.SplitHostPort(s); err == nil {
		host = h
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	a = a.WithZone("")
	return a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast()
}

// reverseName returns the in-addr.arpa name for an IPv4 address, or "".
func reverseName(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil || !a.Is4() {
		return ""
	}
	b := a.As4()
	return net.IPv4(b[3], b[2], b[1], b[0]).String() + ".in-addr.arpa."
}

// usableDNSServer skips empty, unspecified and the deprecated fec0::
// site-local addresses Windows lists for adapters with no IPv6 DNS server.
func usableDNSServer(s string) bool {
	host := s
	if h, _, err := net.SplitHostPort(s); err == nil {
		host = h
	}
	a, err := netip.ParseAddr(host)
	if err != nil || a.IsUnspecified() {
		return false
	}
	return !netip.MustParsePrefix("fec0::/10").Contains(a.WithZone(""))
}

// parseResolvConf returns the nameserver addresses in a resolv.conf.
func parseResolvConf(r io.Reader) []string {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[0] == "nameserver" {
			out = append(out, f[1]) // keep a link-local zone (fe80::1%en0): it is needed to dial
		}
	}
	return out
}

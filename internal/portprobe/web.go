package portprobe

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"netdiag/internal/model"
)

// WebTimeout bounds a whole web request.
var WebTimeout = 3 * time.Second

const (
	maxBody      = 256 * 1024
	maxRedirects = 3
)

// ProbeWeb fetches scheme://ip:port/ and fingerprints the response.
func ProbeWeb(ctx context.Context, ip string, port int, scheme string) model.WebService {
	u := scheme + "://" + net.JoinHostPort(ip, strconv.Itoa(port)) + "/"
	ws := model.WebService{Port: port, Scheme: scheme, URL: u}

	tr := &http.Transport{
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true}, // probe only
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   WebTimeout,
		ResponseHeaderTimeout: WebTimeout,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport: tr,
		Timeout:   WebTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return errors.New("too many redirects")
			}
			if req.URL.Hostname() != via[0].URL.Hostname() {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	cctx, cancel := context.WithTimeout(ctx, WebTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u, nil)
	if err != nil {
		ws.Error = err.Error()
		return ws
	}
	req.Header.Set("User-Agent", "netdiag/1.0")
	resp, err := client.Do(req)
	if err != nil {
		ws.Error = err.Error()
		return ws
	}
	defer resp.Body.Close()
	ws.Status = resp.StatusCode
	ws.Server = resp.Header.Get("Server")
	if resp.Request != nil && resp.Request.URL != nil {
		ws.URL = resp.Request.URL.String()
	}
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		ws.TLS = certInfo(resp.TLS.PeerCertificates[0], time.Now())
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	ws.Title = ExtractTitle(body)
	return ws
}

// ProbeAll probes every open web port concurrently; results ordered by port.
func ProbeAll(ctx context.Context, ip string, open []int) []model.WebService {
	var targets []int
	for _, p := range open {
		if SchemeFor(p) != "" {
			targets = append(targets, p)
		}
	}
	out := make([]model.WebService, len(targets))
	var wg sync.WaitGroup
	for i, p := range targets {
		wg.Add(1)
		go func(i, p int) {
			defer wg.Done()
			out[i] = ProbeWeb(ctx, ip, p, SchemeFor(p))
		}(i, p)
	}
	wg.Wait()
	return out
}

// ExtractTitle returns the whitespace-normalized first <title> text.
func ExtractTitle(b []byte) string {
	z := html.NewTokenizer(strings.NewReader(string(b)))
	in := false
	var sb strings.Builder
	for {
		switch z.Next() {
		case html.ErrorToken:
			return norm(cutTag(sb.String()))
		case html.StartTagToken:
			if n, _ := z.TagName(); string(n) == "title" {
				in = true
			}
		case html.EndTagToken:
			if n, _ := z.TagName(); in && string(n) == "title" {
				return norm(cutTag(sb.String()))
			}
		case html.TextToken:
			if in {
				sb.WriteString(string(z.Text()))
				if sb.Len() > 512 {
					return norm(cutTag(sb.String()))
				}
			}
		}
		if in {
			// tolerate unclosed title: stop at body start
			continue
		}
	}
}

func norm(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func certInfo(c *x509.Certificate, now time.Time) *model.TLSInfo {
	// Self-signed = raw issuer DN equals raw subject DN. CheckSignatureFrom is
	// deliberately not used: it rejects non-CA leaf certs (no BasicConstraints
	// CA:TRUE), which is the common case for printer/embedded devices.
	self := bytes.Equal(c.RawSubject, c.RawIssuer)
	return &model.TLSInfo{
		Subject:    c.Subject.String(),
		Issuer:     c.Issuer.String(),
		NotAfter:   c.NotAfter.UTC().Format(time.RFC3339),
		SelfSigned: self,
		Expired:    now.After(c.NotAfter),
	}
}

func cutTag(s string) string {
	if i := strings.IndexByte(s, '<'); i >= 0 {
		return s[:i]
	}
	return s
}

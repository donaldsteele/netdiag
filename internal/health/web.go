package health

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"netdiag/internal/model"
)

const (
	// webTimeout bounds the whole web server check, redirects included.
	webTimeout = 5 * time.Second
	// maxWebRedirects is the most same-server redirects the web server
	// check follows.
	maxWebRedirects = 5
)

// checkWeb fetches the configured web server URL. It follows redirects
// that stay on the same server (an http to https upgrade or another port
// on the same name), because that is what a browser does with them. A
// redirect to another server is reported, not followed: the page you set
// is then not the one that answers.
//
// A certificate the user chose to trust (Config.TrustedCert) is accepted
// by its SHA-256 fingerprint; any other certificate is verified as usual.
// When verification fails, the result carries the presented certificate
// (data.certSHA256, certSubject, certExpires) so the page can offer to
// trust it, unless trusting it would stop the trusted one from working
// (see certFailure).
//
// A password written in the URL (http://user:pass@host/) never leaves the
// check: every URL in its detail and data is redacted (user:xxxxx@host),
// because the result reaches the page, history files, exports and MCP.
func (r *Runner) checkWeb(ctx context.Context, in Input) model.HealthCheck {
	cfg := in.Config
	raw := strings.TrimSpace(cfg.WebURL)
	if raw == "" {
		c := mk(3, model.HealthInfo, "not configured", 0)
		c.Data = map[string]any{"configured": false}
		return c
	}
	want := cfg.ExpectStatus
	if want == 0 {
		want = 200
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return webData(mk(3, model.HealthFail, "invalid URL: "+redactErr(err).Error(), 0), raw)
	}
	host := req.URL.Hostname()
	trusted := ""
	if cfg.TrustedCert != "" {
		trusted, _ = model.NormalizeFingerprint(cfg.TrustedCert)
	}
	// When the expected status is itself a redirect, the redirect is the
	// answer being checked, so it is not followed.
	follow := want < 300 || want > 399
	tooMany := false
	// fetching is the address being fetched; pinnedAt is the host:port of
	// an earlier step of a redirect chain that presented the trusted
	// certificate.
	fetching, pinnedAt := req.URL, ""
	tr := r.webTransport(host, trusted)
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: webTimeout,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if prev := next.Response; trusted != "" && prev != nil && leafIs(prev.TLS, trusted) {
				pinnedAt = hostPort(via[len(via)-1].URL)
			}
			if !follow || !strings.EqualFold(next.URL.Hostname(), host) {
				return http.ErrUseLastResponse
			}
			if len(via) > maxWebRedirects {
				tooMany = true
				return http.ErrUseLastResponse
			}
			fetching = next.URL
			return nil
		}}
	t0 := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		if c, ok := certFailure(err, raw, fetching, pinnedAt, trusted); ok {
			return c
		}
		return webData(mk(3, model.HealthFail, "request failed: "+redactErr(err).Error(), 0), raw)
	}
	defer resp.Body.Close()
	var body []byte
	if cfg.MustContain != "" {
		body, _ = io.ReadAll(io.LimitReader(resp.Body, maxBody))
	}
	d := time.Since(t0)
	final, redirected := "", false
	if resp.Request != nil && resp.Request.URL != nil {
		final, redirected = resp.Request.URL.Redacted(), resp.Request.URL.String() != req.URL.String()
	}
	at := ""
	if redirected {
		at = ", redirected to " + final
	}
	c := webData(mk(3, model.HealthOK, fmt.Sprintf("HTTP %d in %.0f ms%s", resp.StatusCode, ms(d), at), ms(d)), raw)
	c.Data["status"] = resp.StatusCode
	if redirected {
		c.Data["finalURL"] = final
	}
	// Only a certificate trusted by fingerprint can be out of date here:
	// normal verification refuses an expired one.
	var expired *x509.Certificate
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		leaf := resp.TLS.PeerCertificates[0]
		if trusted != "" && Fingerprint(leaf) == trusted {
			c.Data["trustedCert"] = true
			if time.Now().After(leaf.NotAfter) {
				expired = leaf
			}
		}
	}
	switch {
	case resp.StatusCode != want:
		c.Status = model.HealthFail
		c.Detail = fmt.Sprintf("HTTP %d (expected %d)%s", resp.StatusCode, want, at)
		if loc, err := resp.Location(); err == nil && resp.StatusCode >= 300 && resp.StatusCode <= 399 {
			shown := loc.Redacted()
			c.Data["location"] = shown
			switch {
			case tooMany:
				c.Detail += fmt.Sprintf(": more than %d redirects, the last one to %s", maxWebRedirects, shown)
			case !strings.EqualFold(loc.Hostname(), host):
				c.Detail += ": it redirects to another server, " + shown
			default:
				c.Detail += ": it redirects to " + shown
			}
		}
	case cfg.MustContain != "" && !strings.Contains(string(body), cfg.MustContain):
		c.Status = model.HealthFail
		c.Detail = fmt.Sprintf("HTTP %d but page does not contain %q%s", resp.StatusCode, cfg.MustContain, at)
	case expired != nil:
		c.Status = model.HealthWarn
		c.Detail += "; the certificate you trusted expired on " + certDate(expired.NotAfter)
	case d > 2*time.Second:
		c.Status = model.HealthWarn
		c.Detail += " (slow)"
	}
	return c
}

// webData sets the data every configured web server result carries. The
// URL is stored with any password hidden.
func webData(c model.HealthCheck, raw string) model.HealthCheck {
	c.Data = map[string]any{"configured": true, "url": model.RedactURL(raw)}
	return c
}

// redactErr hides a password in the URL a request error names. The
// http.Client already replaces it with "***", but an error from parsing
// the URL shows it in clear.
func redactErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = model.RedactURL(ue.URL)
	}
	return err
}

// webTransport returns a fresh transport for one web server check, with
// the same proxy and timeouts as the default one. With a trusted
// fingerprint, that certificate is accepted on host and any other is
// verified as usual.
func (r *Runner) webTransport(host, trusted string) *http.Transport {
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = dt.Clone()
	}
	tc := &tls.Config{RootCAs: r.roots}
	if trusted != "" {
		roots := r.roots
		tc.InsecureSkipVerify = true // VerifyConnection checks the certificate instead
		tc.VerifyConnection = func(cs tls.ConnectionState) error {
			return verifyTrusted(cs, host, trusted, roots)
		}
	}
	tr.TLSClientConfig = tc
	return tr
}

// verifyTrusted accepts the connection when the server's certificate is
// the trusted one, and otherwise verifies it the way crypto/tls would.
func verifyTrusted(cs tls.ConnectionState, host, trusted string, roots *x509.CertPool) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("tls: the server sent no certificate")
	}
	leaf := cs.PeerCertificates[0]
	if Fingerprint(leaf) == trusted {
		return nil
	}
	inter := x509.NewCertPool()
	for _, c := range cs.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, DNSName: host})
	if err != nil {
		return &tls.CertificateVerificationError{UnverifiedCertificates: cs.PeerCertificates, Err: err}
	}
	return nil
}

// Fingerprint is the SHA-256 of the certificate, as 64 lowercase hex
// digits (the form settings store).
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

// certFailure turns a certificate verification error at the address at
// into a failed check that names the problem in plain words and carries
// the certificate. pinnedAt is the host:port where an earlier step of the
// same check was answered with the trusted certificate, if any.
//
// With a trusted certificate, a different one at the same address means
// it changed since the user trusted it. A different one at another address
// of the same server (a redirect to another https port) is reported as
// that: only one certificate can be trusted, so the result does not offer
// to trust this one (no certSHA256), which would stop the trusted one
// from working.
func certFailure(err error, raw string, at *url.URL, pinnedAt, trusted string) (model.HealthCheck, bool) {
	var cve *tls.CertificateVerificationError
	if !errors.As(err, &cve) || len(cve.UnverifiedCertificates) == 0 {
		return model.HealthCheck{}, false
	}
	leaf := cve.UnverifiedCertificates[0]
	fp := Fingerprint(leaf)
	reason := certReason(cve.Err, leaf, at.Hostname())
	detail := "the certificate is not trusted: " + reason
	offer := true
	if trusted != "" && fp != trusted {
		if pinnedAt != "" && pinnedAt != hostPort(at) {
			detail = fmt.Sprintf("another address on this server, %s, uses a different certificate that is not trusted (%s); "+
				"netdiag trusts one certificate for the web server, so to check that address set it as the Web server URL", at.Redacted(), reason)
			offer = false
		} else {
			detail = "the certificate changed since you trusted it: trust the new one only if you know your server has a new certificate"
		}
	}
	c := webData(mk(3, model.HealthFail, detail, 0), raw)
	if offer {
		c.Data["certSHA256"] = fp
	}
	c.Data["certSubject"] = certName(leaf)
	c.Data["certExpires"] = leaf.NotAfter.UTC().Format(time.RFC3339)
	return c, true
}

// leafIs reports whether the connection's certificate has the fingerprint fp.
func leafIs(cs *tls.ConnectionState, fp string) bool {
	return cs != nil && len(cs.PeerCertificates) > 0 && Fingerprint(cs.PeerCertificates[0]) == fp
}

// hostPort is the lower-case host and port u connects to, with the
// scheme's default port when u names none.
func hostPort(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "80"
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		}
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// certReason says why verification refused leaf.
func certReason(err error, leaf *x509.Certificate, host string) string {
	var inv x509.CertificateInvalidError
	var hn x509.HostnameError
	switch {
	case errors.As(err, &inv) && inv.Reason == x509.Expired:
		if time.Now().Before(leaf.NotBefore) {
			return "it is not valid until " + certDate(leaf.NotBefore)
		}
		return "it expired on " + certDate(leaf.NotAfter)
	case errors.As(err, &hn):
		return fmt.Sprintf("it is made out to %s, not %s", certNames(leaf), host)
	}
	return "self-signed or from a private authority"
}

// certName is the certificate's common name, or its whole subject.
func certName(c *x509.Certificate) string {
	if c.Subject.CommonName != "" {
		return c.Subject.CommonName
	}
	return c.Subject.String()
}

// certNames lists the names a certificate is for (at most three).
func certNames(c *x509.Certificate) string {
	var names []string
	names = append(names, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		names = append(names, ip.String())
	}
	if len(names) == 0 {
		if c.Subject.CommonName == "" {
			return "no name"
		}
		names = []string{c.Subject.CommonName}
	}
	if len(names) > 3 {
		names = append(names[:3], "...")
	}
	return strings.Join(names, ", ")
}

func certDate(t time.Time) string { return t.UTC().Format("2 Jan 2006") }

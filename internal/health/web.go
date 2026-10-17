package health

import (
	"context"
	"fmt"
	"io"
	"net/http"
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
func (r *Runner) checkWeb(ctx context.Context, in Input) model.HealthCheck {
	cfg := in.Config
	raw := strings.TrimSpace(cfg.WebURL)
	if raw == "" {
		c := mk(3, model.HealthInfo, "no web server configured (set one in Settings)", 0)
		c.Data = map[string]any{"configured": false}
		return c
	}
	want := cfg.ExpectStatus
	if want == 0 {
		want = 200
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return webData(mk(3, model.HealthFail, "invalid URL: "+err.Error(), 0), raw)
	}
	host := req.URL.Hostname()
	// When the expected status is itself a redirect, the redirect is the
	// answer being checked, so it is not followed.
	follow := want < 300 || want > 399
	tooMany := false
	tr := r.webTransport()
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: webTimeout,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if !follow || !strings.EqualFold(next.URL.Hostname(), host) {
				return http.ErrUseLastResponse
			}
			if len(via) > maxWebRedirects {
				tooMany = true
				return http.ErrUseLastResponse
			}
			return nil
		}}
	t0 := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return webData(mk(3, model.HealthFail, "request failed: "+err.Error(), 0), raw)
	}
	defer resp.Body.Close()
	var body []byte
	if cfg.MustContain != "" {
		body, _ = io.ReadAll(io.LimitReader(resp.Body, maxBody))
	}
	d := time.Since(t0)
	final := raw
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	redirected := final != req.URL.String()
	at := ""
	if redirected {
		at = ", redirected to " + final
	}
	c := webData(mk(3, model.HealthOK, fmt.Sprintf("HTTP %d in %.0f ms%s", resp.StatusCode, ms(d), at), ms(d)), raw)
	c.Data["status"] = resp.StatusCode
	if redirected {
		c.Data["finalURL"] = final
	}
	switch {
	case resp.StatusCode != want:
		c.Status = model.HealthFail
		c.Detail = fmt.Sprintf("HTTP %d (expected %d)%s", resp.StatusCode, want, at)
		if loc, err := resp.Location(); err == nil && resp.StatusCode >= 300 && resp.StatusCode <= 399 {
			c.Data["location"] = loc.String()
			switch {
			case tooMany:
				c.Detail += fmt.Sprintf(": more than %d redirects, the last one to %s", maxWebRedirects, loc)
			case !strings.EqualFold(loc.Hostname(), host):
				c.Detail += ": it redirects to another server, " + loc.String()
			default:
				c.Detail += ": it redirects to " + loc.String()
			}
		}
	case cfg.MustContain != "" && !strings.Contains(string(body), cfg.MustContain):
		c.Status = model.HealthFail
		c.Detail = fmt.Sprintf("HTTP %d but page does not contain %q%s", resp.StatusCode, cfg.MustContain, at)
	case d > 2*time.Second:
		c.Status = model.HealthWarn
		c.Detail += " (slow)"
	}
	return c
}

// webData sets the data every configured web server result carries.
func webData(c model.HealthCheck, url string) model.HealthCheck {
	c.Data = map[string]any{"configured": true, "url": url}
	return c
}

// webTransport returns a fresh transport for one web server check, with
// the same proxy and timeouts as the default one.
func (r *Runner) webTransport() *http.Transport {
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		return dt.Clone()
	}
	return &http.Transport{Proxy: http.ProxyFromEnvironment}
}

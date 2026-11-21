package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"netdiag/internal/model"
	"netdiag/internal/tailscale"
)

// TailnetSource reads this computer's Tailscale service (tailscale.Client;
// tests fake it).
type TailnetSource interface {
	Status(ctx context.Context) (tailscale.Status, error)
	Ping(ctx context.Context, ip string) (tailscale.PingResult, error)
}

// tailnetCacheFor is how long a read of the Tailscale service is reused, so
// the page, the Health view and Claude asking at once read it once.
const tailnetCacheFor = 3 * time.Second

// Ping limits: one device at most every 5 s, and one ping a second overall.
const (
	tailnetPingPerDevice = 5 * time.Second
	tailnetPingGap       = time.Second
)

// ErrTailnetPingTooSoon is returned when a ping comes too soon after the last.
var ErrTailnetPingTooSoon = errors.New("wait a few seconds before checking this device again")

// ErrNotTailscaleAddr is returned for a ping to an address that is not on the tailnet.
var ErrNotTailscaleAddr = errors.New("that is not one of this tailnet's addresses")

// tailnetState is the Service's cache and ping limiter.
type tailnetState struct {
	mu       sync.Mutex
	cached   model.TailnetView
	cachedAt time.Time
	lastPing time.Time
	pinged   map[string]time.Time
}

func (s *Service) tailnetSource() TailnetSource {
	if s.cfg.Tailnet != nil {
		return s.cfg.Tailnet
	}
	return tailscale.New()
}

func (s *Service) now() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now()
}

// Tailnet reads this computer's Tailscale state and tailnet (GET
// /api/tailnet). It never fails: a computer without Tailscale gets state
// not-installed, and a service that answers with an error gets the error in
// words on the Tailscale card.
func (s *Service) Tailnet(ctx context.Context) model.TailnetView {
	s.ts.mu.Lock()
	if !s.ts.cachedAt.IsZero() && s.now().Sub(s.ts.cachedAt) < tailnetCacheFor {
		v := s.ts.cached
		s.ts.mu.Unlock()
		return v
	}
	s.ts.mu.Unlock()
	st, err := s.tailnetSource().Status(ctx)
	v := TailnetViewOf(st, err, s.now())
	s.ts.mu.Lock()
	s.ts.cached, s.ts.cachedAt = v, s.now()
	s.ts.mu.Unlock()
	return v
}

// PingTailnet pings one tailnet device through Tailscale (POST
// /api/tailnet/ping): one device, at most every 5 s, at most one a second.
func (s *Service) PingTailnet(ctx context.Context, ip string) (model.TailnetPing, error) {
	v := s.Tailnet(ctx)
	if v.State != model.TailnetRunning {
		return model.TailnetPing{}, errors.New("Tailscale isn't connected on this computer")
	}
	if !tailnetHasIP(v, ip) {
		return model.TailnetPing{}, ErrNotTailscaleAddr
	}
	now := s.now()
	s.ts.mu.Lock()
	if s.ts.pinged == nil {
		s.ts.pinged = map[string]time.Time{}
	}
	if last, ok := s.ts.pinged[ip]; ok && now.Sub(last) < tailnetPingPerDevice || now.Sub(s.ts.lastPing) < tailnetPingGap {
		s.ts.mu.Unlock()
		return model.TailnetPing{}, ErrTailnetPingTooSoon
	}
	s.ts.pinged[ip], s.ts.lastPing = now, now
	for k, t := range s.ts.pinged {
		if now.Sub(t) > time.Minute {
			delete(s.ts.pinged, k)
		}
	}
	s.ts.mu.Unlock()
	res, err := s.tailnetSource().Ping(ctx, ip)
	if err != nil {
		return model.TailnetPing{}, err
	}
	return model.TailnetPing{IP: res.IP, Name: res.Name, OK: res.OK, Error: res.Error, LatencyMs: res.Latency,
		Path: res.Path, Endpoint: res.Endpoint, Relay: res.Relay}, nil
}

func tailnetHasIP(v model.TailnetView, ip string) bool {
	for _, d := range v.Devices {
		for _, a := range d.IPs {
			if a == ip {
				return true
			}
		}
	}
	return false
}

// TailnetViewOf turns a Tailscale reading into the page's view (tailscale.ViewOf).
func TailnetViewOf(st tailscale.Status, err error, now time.Time) model.TailnetView {
	return tailscale.ViewOf(st, err, now)
}

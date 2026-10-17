package scan

import (
	"context"
	"errors"
	"testing"
	"time"

	"netdiag/internal/model"
)

// startLive starts a live scan through reg and waits until it is watching.
func startLive(t *testing.T, reg *Registry, req model.ScanRequest) *Scan {
	t.Helper()
	req.Live = true
	s, err := reg.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	ch, unsub := s.Subscribe()
	waitLive(t, ch, func(model.LiveStatus) bool { return true })
	unsub()
	return s
}

// A scan watching the network is never stopped silently: a new request
// without ReplaceLive gets a *WatchingError naming the live scan, and the
// live scan goes on (F9).
func TestRegistryKeepsWatching(t *testing.T) {
	d := fakeDeps()
	d.LiveInterval = time.Hour
	reg := NewRegistry(New(d))
	s1 := startLive(t, reg, model.ScanRequest{CIDR: "10.0.0.0/28", Interface: "eth0", Community: "s3cr3t"}.WithOrigin(model.OriginUI))
	if s1.LiveSince() == "" {
		t.Fatal("LiveSince not set once live")
	}

	_, err := reg.Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/28"}.WithOrigin(model.OriginMCP))
	var we *WatchingError
	if !errors.As(err, &we) || !errors.Is(err, ErrWatching) || errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want a WatchingError", err)
	}
	if *we != (WatchingError{ID: s1.ID, CIDR: "10.0.0.0/28", Interface: "eth0", Since: s1.LiveSince()}) {
		t.Fatalf("watching = %+v", *we)
	}
	if s1.State() != model.StateLive {
		t.Fatalf("live scan state = %q", s1.State())
	}
	if got, ok := reg.Running(); !ok || got != s1 {
		t.Fatal("the live scan is no longer the running one")
	}
	if _, err := reg.Start(context.Background(), model.ScanRequest{CIDR: "bogus", ReplaceLive: true}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("bad request: err = %v", err)
	}
	if s1.State() != model.StateLive {
		t.Fatal("a bad request stopped the live scan")
	}
	if q := s1.Request(); q.Origin() != model.OriginUI || q.Community != model.Redacted {
		t.Fatalf("Request() = %+v (origin %q)", q, q.Origin())
	}
	s1.Cancel()
	s1.Wait()
}

// ReplaceLive (Restart watching) cancels the live scan and starts the new
// one; the flag is not kept in the new scan's request.
func TestRegistryReplaceLive(t *testing.T) {
	d := fakeDeps()
	d.LiveInterval = time.Hour
	reg := NewRegistry(New(d))
	s1 := startLive(t, reg, model.ScanRequest{CIDR: "10.0.0.0/28"})
	s2, err := reg.Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/28", ReplaceLive: true})
	if err != nil {
		t.Fatal(err)
	}
	if s1.State() != model.StateDone {
		t.Fatalf("old scan state = %q", s1.State())
	}
	if got, ok := reg.Running(); !ok || got != s2 {
		t.Fatal("the new scan is not the running one")
	}
	if s2.Request().ReplaceLive || s2.Snapshot().Request.ReplaceLive {
		t.Fatal("replaceLive kept in the new scan's request")
	}
	s2.Wait()
}

// A scan still in its first pass is busy, with or without ReplaceLive.
func TestRegistryInitialPhaseBusy(t *testing.T) {
	d := fakeDeps()
	d.Pinger = fakePinger{block: true}
	d.Sweep.Timeout = 10 * time.Second
	reg := NewRegistry(New(d))
	s1, err := reg.Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/30", Live: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, replace := range []bool{false, true} {
		_, err := reg.Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/30", ReplaceLive: replace})
		if !errors.Is(err, ErrBusy) || errors.Is(err, ErrWatching) {
			t.Fatalf("replaceLive=%v: err = %v, want ErrBusy", replace, err)
		}
	}
	if s1.LiveSince() != "" {
		t.Fatal("LiveSince set before the scan went live")
	}
	s1.Cancel()
	<-s1.Done()
}

package scan

import (
	"context"
	"errors"
	"sync"
	"time"

	"netdiag/internal/model"
)

// MaxRetained is the number of scans kept in the registry.
const MaxRetained = 10

// ErrBusy is returned by Registry.Start while another scan is running.
var ErrBusy = errors.New("a scan is already running")

// ErrWatching is matched (errors.Is) by the *WatchingError Registry.Start
// returns while a scan is watching the network and the new request does not
// set ReplaceLive.
var ErrWatching = errors.New("netdiag is watching this network")

// WatchingError describes the live scan that Registry.Start refused to stop.
type WatchingError struct {
	ID        string `json:"id"`
	CIDR      string `json:"cidr"`
	Interface string `json:"interface"`
	Since     string `json:"since"` // when it went live (RFC 3339)
}

func (e *WatchingError) Error() string { return ErrWatching.Error() }

// Is makes errors.Is(err, ErrWatching) true.
func (e *WatchingError) Is(target error) bool { return target == ErrWatching }

// Registry allows one running scan at a time and retains the last
// MaxRetained scans for lookup.
type Registry struct {
	o *Orchestrator

	mu      sync.Mutex
	scans   map[string]*Scan
	order   []string // oldest first
	running *Scan
}

// NewRegistry returns a registry backed by o.
func NewRegistry(o *Orchestrator) *Registry {
	return &Registry{o: o, scans: map[string]*Scan{}}
}

// liveStopTimeout bounds how long Start waits for a live scan to stop.
const liveStopTimeout = 10 * time.Second

// Start launches a scan. A running scan in live state (initial scan
// complete) is never stopped silently: without req.ReplaceLive Start returns
// a *WatchingError and the live scan goes on; with it, the live scan is
// cancelled and awaited first. Start returns ErrBusy if a scan is still in
// its initial phase, or an error wrapping ErrInvalidRequest for a bad
// request. The scan's lifetime is bound to ctx.
func (r *Registry) Start(ctx context.Context, req model.ScanRequest) (*Scan, error) {
	if _, _, err := Validate(req); err != nil {
		return nil, err // never stop a live scan for a bad request
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running != nil {
		select {
		case <-r.running.Done():
		default:
			if r.running.State() != model.StateLive {
				return nil, ErrBusy
			}
			if !req.ReplaceLive {
				return nil, watching(r.running)
			}
			r.running.Cancel()
			select {
			case <-r.running.Done():
			case <-time.After(liveStopTimeout):
				return nil, ErrBusy
			}
		}
	}
	req.ReplaceLive = false // done its job; keep it out of the saved request
	s, err := r.o.Start(ctx, req)
	if err != nil {
		return nil, err
	}
	r.running = s
	r.scans[s.ID] = s
	r.order = append(r.order, s.ID)
	for len(r.order) > MaxRetained {
		delete(r.scans, r.order[0])
		r.order = r.order[1:]
	}
	return s, nil
}

func watching(s *Scan) *WatchingError {
	q := s.Request()
	return &WatchingError{ID: s.ID, CIDR: q.CIDR, Interface: q.Interface, Since: s.LiveSince()}
}

// Get returns the scan with id.
func (r *Registry) Get(id string) (*Scan, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.scans[id]
	return s, ok
}

// Cancel cancels the scan with id; it reports whether the scan exists.
func (r *Registry) Cancel(id string) bool {
	s, ok := r.Get(id)
	if ok {
		s.Cancel()
	}
	return ok
}

// Running returns the currently running scan, if any.
func (r *Registry) Running() (*Scan, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running == nil {
		return nil, false
	}
	select {
	case <-r.running.Done():
		return nil, false
	default:
		return r.running, true
	}
}

// List returns retained scans, oldest first.
func (r *Registry) List() []*Scan {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Scan, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.scans[id])
	}
	return out
}

// Orchestrator returns the orchestrator backing the registry.
func (r *Registry) Orchestrator() *Orchestrator { return r.o }

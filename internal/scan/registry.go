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

// Start launches a scan. A running scan in live state (initial scan complete)
// is cancelled and awaited first. It returns ErrBusy if a scan is still in
// its initial phase, or an error
// wrapping ErrInvalidRequest for a bad request. The scan's lifetime is bound
// to ctx.
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
			r.running.Cancel()
			select {
			case <-r.running.Done():
			case <-time.After(liveStopTimeout):
				return nil, ErrBusy
			}
		}
	}
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

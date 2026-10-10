package service

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"netdiag/internal/model"
	"netdiag/internal/scan"
)

// Event log limits.
const (
	EventRingCap      = 1000
	DefaultEventLimit = 100
	MaxEventLimit     = 500
	maxEventLogsKept  = 8
	lazySettleIdle    = 40 * time.Millisecond
	lazySettleMaxWait = 750 * time.Millisecond
)

// AgentEvent is one compact event for agent polling.
type AgentEvent struct {
	Seq     int64  `json:"seq"`
	At      string `json:"at"`
	Type    string `json:"type"`
	IP      string `json:"ip,omitempty"`
	Summary string `json:"summary"`
	Replay  bool   `json:"replay,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// PrinterEventData is the small payload of a "printer" event.
type PrinterEventData struct {
	State       string   `json:"state,omitempty"`
	Alerts      []string `json:"alerts"`
	LowSupplies []string `json:"lowSupplies"`
}

// EventLog is a bounded per-scan log of agent-relevant events.
type EventLog struct {
	mu         sync.RWMutex
	ring       []AgentEvent
	nextSeq    int64
	seen       map[string]bool
	printerSig map[string]string
	lastHealth string
	lazy       bool
	finished   bool // scan was already finished when attached lazily
	first      bool // no event ingested yet
	isDone     bool
	lastIngest time.Time
	closed     chan struct{}
}

func newEventLog(lazy bool) *EventLog {
	return &EventLog{
		nextSeq:    1,
		seen:       map[string]bool{},
		printerSig: map[string]string{},
		lazy:       lazy,
		first:      true,
		closed:     make(chan struct{}),
	}
}

func (l *EventLog) add(typ, ip, summary string, replay bool, data any) {
	e := AgentEvent{
		Seq: l.nextSeq, At: time.Now().UTC().Format(time.RFC3339),
		Type: typ, IP: ip, Summary: summary, Replay: replay, Data: data,
	}
	l.nextSeq++
	l.ring = append(l.ring, e)
	if len(l.ring) > EventRingCap {
		l.ring = slices.Clone(l.ring[len(l.ring)-EventRingCap:])
	}
}

// Since returns events with Seq > sinceSeq (up to limit) and the cursor to
// pass next time. dropped is true when events were evicted from the ring
// before the caller saw them.
func (l *EventLog) Since(sinceSeq int64, limit int) (events []AgentEvent, nextSeq int64, dropped bool) {
	if limit <= 0 {
		limit = DefaultEventLimit
	}
	if limit > MaxEventLimit {
		limit = MaxEventLimit
	}
	if sinceSeq < 0 {
		sinceSeq = 0
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	latest := l.nextSeq - 1
	if len(l.ring) > 0 && sinceSeq < l.ring[0].Seq-1 {
		dropped = true
	}
	events = []AgentEvent{}
	for _, e := range l.ring {
		if e.Seq > sinceSeq {
			if len(events) >= limit {
				break
			}
			events = append(events, e)
		}
	}
	switch {
	case len(events) > 0:
		nextSeq = events[len(events)-1].Seq
	case sinceSeq > latest:
		nextSeq = latest
	default:
		nextSeq = sinceSeq
	}
	return events, nextSeq, dropped
}

// Finished reports whether the terminal done event was recorded.
func (l *EventLog) Finished() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.isDone
}

// seed records baseline state from an already-known scan result so replayed
// events produce no noise.
func (l *EventLog) seed(r model.ScanResult) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, h := range r.Hosts {
		l.seen[h.IP] = true
		if h.Printer != nil {
			l.printerSig[h.IP], _ = printerSignature(h.Printer)
		}
	}
	if r.Health != nil {
		l.lastHealth = healthSummary(*r.Health)
	}
	l.finished = r.State == model.StateDone || r.State == model.StateCancelled || r.State == model.StateError
}

func lowSupplies(p *model.PrinterInfo) []string {
	out := []string{}
	for _, s := range p.Supplies {
		if s.State == "low" || s.State == "empty" {
			out = append(out, fmt.Sprintf("%s %d%%", s.Description, s.Percent))
		}
	}
	return out
}

// printerSignature returns a comparable signature and event data.
func printerSignature(p *model.PrinterInfo) (string, PrinterEventData) {
	d := PrinterEventData{State: p.State, Alerts: []string{}, LowSupplies: lowSupplies(p)}
	for _, a := range p.Alerts {
		d.Alerts = append(d.Alerts, a.Description)
	}
	al := slices.Clone(d.Alerts)
	slices.Sort(al)
	sup := slices.Clone(d.LowSupplies)
	slices.Sort(sup)
	return p.State + "|" + strings.Join(al, ";") + "|" + strings.Join(sup, ";"), d
}

func healthSummary(h model.HealthReport) string {
	fail, warn := 0, 0
	for _, c := range h.Checks {
		switch c.Status {
		case model.HealthFail:
			fail++
		case model.HealthWarn:
			warn++
		}
	}
	if fail == 0 && warn == 0 {
		return "all ok"
	}
	return fmt.Sprintf("%d fail, %d warn", fail, warn)
}

func asHost(v any) (model.Host, bool) {
	switch h := v.(type) {
	case model.Host:
		return h, true
	case *model.Host:
		if h != nil {
			return *h, true
		}
	}
	return model.Host{}, false
}

// ingest converts one bus event into zero or more agent events.
func (l *EventLog) ingest(ev model.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	first := l.first
	l.first = false
	l.lastIngest = time.Now()

	switch ev.Type {
	case model.EventStage:
		st, ok := ev.Data.(model.StageInfo)
		if !ok {
			return
		}
		replay := false
		if l.lazy && first {
			if l.finished {
				return
			}
			replay = true
		}
		l.add("stage", "", "stage: "+st.Stage, replay, map[string]string{"stage": st.Stage})
	case model.EventHost:
		h, ok := asHost(ev.Data)
		if !ok {
			return
		}
		if !l.seen[h.IP] {
			l.seen[h.IP] = true
			name := h.Hostname
			if name == "" {
				name = h.Vendor
			}
			s := "discovered " + h.IP
			if name != "" {
				s += " (" + name + ")"
			}
			l.add("discovered", h.IP, s, false, map[string]any{
				"mac": h.MAC, "vendor": h.Vendor, "hostname": h.Hostname,
				"deviceType": h.DeviceType, "isPrinter": h.IsPrinter,
			})
		}
		if h.Printer != nil {
			sig, d := printerSignature(h.Printer)
			prev, had := l.printerSig[h.IP]
			l.printerSig[h.IP] = sig
			if sig != prev {
				problem := len(d.Alerts) > 0 || len(d.LowSupplies) > 0 || d.State == "stopped"
				if had || problem {
					st := d.State
					if st == "" {
						st = "-"
					}
					s := fmt.Sprintf("printer %s: state=%s", h.IP, st)
					if len(d.Alerts) > 0 {
						s += ", alerts: " + strings.Join(d.Alerts, "; ")
					}
					if len(d.LowSupplies) > 0 {
						s += ", low: " + strings.Join(d.LowSupplies, ", ")
					}
					l.add("printer", h.IP, s, false, d)
				}
			}
		}
	case model.EventPresence:
		p, ok := ev.Data.(model.Presence)
		if !ok {
			return
		}
		if p.Online {
			l.add("online", p.IP, "back online: "+p.IP, false, map[string]string{"reason": p.Reason})
		} else {
			l.add("offline", p.IP, "went offline: "+p.IP, false, map[string]string{"reason": p.Reason})
		}
	case model.EventConflict:
		c, ok := ev.Data.(model.ConflictEvent)
		if !ok {
			return
		}
		typ, verb := "conflict", "conflict"
		if c.Resolved {
			typ, verb = "conflict_resolved", "conflict resolved"
		}
		ip := ""
		if len(c.Conflict.IPs) > 0 {
			ip = c.Conflict.IPs[0]
		}
		s := fmt.Sprintf("%s (%s): %s", verb, c.Conflict.Kind, c.Conflict.Detail)
		l.add(typ, ip, s, c.Replay, map[string]any{
			"id": c.Conflict.ID, "kind": c.Conflict.Kind, "severity": c.Conflict.Severity,
			"ips": c.Conflict.IPs, "macs": c.Conflict.MACs, "name": c.Conflict.Name,
		})
	case model.EventHealth:
		var h model.HealthReport
		switch v := ev.Data.(type) {
		case model.HealthReport:
			h = v
		case *model.HealthReport:
			if v == nil {
				return
			}
			h = *v
		default:
			return
		}
		sum := healthSummary(h)
		if sum == l.lastHealth {
			return
		}
		l.lastHealth = sum
		failing := []map[string]string{}
		for _, c := range h.Checks {
			if c.Status == model.HealthFail || c.Status == model.HealthWarn {
				failing = append(failing, map[string]string{"check": c.ID, "status": c.Status, "detail": c.Detail})
			}
		}
		l.add("health", "", "health: "+sum, false, map[string]any{"summary": sum, "failing": failing})
	case model.EventNotice:
		n, ok := ev.Data.(model.NoticeEvent)
		if !ok {
			return
		}
		l.add("notice", n.IP, fmt.Sprintf("[%s] %s: %s", n.Severity, n.Title, n.Message), n.Replay, map[string]any{
			"id": n.ID, "trigger": n.Trigger, "severity": n.Severity,
		})
	case model.EventDone:
		l.isDone = true
		data := map[string]any{}
		s := "scan finished"
		var r *model.ScanResult
		switch v := ev.Data.(type) {
		case model.ScanResult:
			r = &v
		case *model.ScanResult:
			r = v
		}
		if r != nil {
			st := r.Stats
			s = fmt.Sprintf("scan %s: %d alive, %d printers, %d web, %d mdns, %d offline, %d conflicts",
				r.State, st.Alive, st.Printers, st.Web, st.MDNS, st.Offline, st.Conflicts)
			data["state"] = r.State
			data["stats"] = st
		}
		l.add("done", "", s, false, data)
	}
}

// attachLog subscribes a log to sc. Lazy logs are seeded from the scan's
// current snapshot so bus replay produces no noise.
func (s *Service) attachLog(sc *scan.Scan, lazy bool) *EventLog {
	snap := sc.Snapshot()
	s.logsMu.Lock()
	if l, ok := s.logs[snap.ID]; ok {
		s.logsMu.Unlock()
		return l
	}
	l := newEventLog(lazy)
	if lazy {
		l.seed(snap)
	}
	s.logs[snap.ID] = l
	s.logOrder = append(s.logOrder, snap.ID)
	s.evictLogsLocked()
	s.logsMu.Unlock()

	ch, cancel := sc.Subscribe()
	go func() {
		defer close(l.closed)
		defer cancel()
		for ev := range ch {
			l.ingest(ev)
		}
	}()
	return l
}

// evictLogsLocked drops the oldest finished logs beyond the keep limit.
func (s *Service) evictLogsLocked() {
	for len(s.logOrder) > maxEventLogsKept {
		idx := -1
		for i, id := range s.logOrder[:len(s.logOrder)-1] {
			if s.logs[id].Finished() {
				idx = i
				break
			}
		}
		if idx < 0 {
			return
		}
		delete(s.logs, s.logOrder[idx])
		s.logOrder = slices.Delete(s.logOrder, idx, idx+1)
	}
}

// Events returns the event log for a scan, attaching lazily when the scan
// is in the registry but has no log yet.
func (s *Service) Events(scanID string) (*EventLog, error) {
	s.logsMu.Lock()
	l, ok := s.logs[scanID]
	s.logsMu.Unlock()
	if ok {
		return l, nil
	}
	sc, found := s.cfg.Registry.Get(scanID)
	if !found {
		return nil, &NotFoundError{Msg: "unknown scan"}
	}
	l = s.attachLog(sc, true)
	l.settle()
	return l, nil
}

// settle waits briefly for the initial bus replay to be consumed: until the
// subscription closes or ingestion has been idle for a short while.
func (l *EventLog) settle() {
	start := time.Now()
	for time.Since(start) < lazySettleMaxWait {
		select {
		case <-l.closed:
			return
		case <-time.After(5 * time.Millisecond):
		}
		l.mu.RLock()
		last := l.lastIngest
		if l.first {
			last = start
		}
		l.mu.RUnlock()
		if time.Since(last) >= lazySettleIdle {
			return
		}
	}
}

package scan

import (
	"slices"
	"sync"

	"netdiag/internal/model"
	"netdiag/internal/report"
)

// Bus is a per-scan fan-out broadcaster. It keeps a view of the latest host
// copies, stage and progress so late subscribers receive a snapshot first and
// then live events. Publish never blocks: every subscriber has an unbounded
// queue drained by its own goroutine.
type Bus struct {
	mu       sync.Mutex
	hosts    map[string]model.Host
	stage    string
	progress map[string]model.Progress
	final    *model.Event
	snap     *model.Event // initial-scan result in live mode
	live     *model.Event // latest live status
	health   *model.Event // latest health report
	conflict map[string]model.ConflictEvent
	notices  []model.Notice // last ReplayNotices
	subs     map[*subscriber]struct{}
}

// NewBus returns an empty bus.
func NewBus() *Bus {
	return &Bus{
		hosts:    map[string]model.Host{},
		progress: map[string]model.Progress{},
		conflict: map[string]model.ConflictEvent{},
		subs:     map[*subscriber]struct{}{},
	}
}

// Publish records ev in the view and delivers it to all subscribers. A done
// or error event is terminal: subscribers' channels are closed after it is
// delivered and later publishes are ignored.
func (b *Bus) Publish(ev model.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.final != nil {
		return
	}
	switch ev.Type {
	case model.EventHost:
		if h, ok := ev.Data.(model.Host); ok {
			b.hosts[h.IP] = h
		}
	case model.EventStage:
		if s, ok := ev.Data.(model.StageInfo); ok {
			b.stage = s.Stage
		}
	case model.EventProgress:
		if p, ok := ev.Data.(model.Progress); ok {
			b.progress[p.Stage] = p
		}
	case model.EventSnapshot:
		e := ev
		b.snap = &e
	case model.EventLive:
		e := ev
		b.live = &e
	case model.EventHealth:
		e := ev
		b.health = &e
	case model.EventConflict:
		if c, ok := ev.Data.(model.ConflictEvent); ok {
			if c.Resolved {
				delete(b.conflict, c.Conflict.ID)
			} else {
				b.conflict[c.Conflict.ID] = c
			}
		}
	case model.EventNotice:
		if n, ok := ev.Data.(model.NoticeEvent); ok {
			b.notices = append(b.notices, n.Notice)
			if len(b.notices) > ReplayNotices {
				b.notices = slices.Clone(b.notices[len(b.notices)-ReplayNotices:])
			}
		}
	case model.EventDone, model.EventError:
		e := ev
		b.final = &e
	}
	for s := range b.subs {
		s.push(ev, b.final != nil)
	}
	if b.final != nil {
		b.subs = map[*subscriber]struct{}{}
	}
}

// Subscribe returns a channel that first replays a snapshot (current stage,
// the live-mode initial result, all hosts in IP order, current-stage
// progress, the latest live status, the latest health report, current
// conflicts and the last ReplayNotices notices (flagged replay), and the terminal event if the scan
// already finished) followed by live events. The channel is closed
// after the terminal event or when the returned cancel func is called.
func (b *Bus) Subscribe() (<-chan model.Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := newSubscriber()
	var replay []model.Event
	if b.stage != "" {
		replay = append(replay, model.Event{Type: model.EventStage, Data: model.StageInfo{Stage: b.stage}})
	}
	if b.snap != nil {
		replay = append(replay, *b.snap)
	}
	for _, h := range b.hostsLocked() {
		replay = append(replay, model.Event{Type: model.EventHost, Data: h})
	}
	if p, ok := b.progress[b.stage]; ok {
		replay = append(replay, model.Event{Type: model.EventProgress, Data: p})
	}
	if b.live != nil {
		replay = append(replay, *b.live)
	}
	if b.health != nil {
		replay = append(replay, *b.health)
	}
	ids := make([]string, 0, len(b.conflict))
	for id := range b.conflict {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		c := b.conflict[id]
		c.Replay = true
		replay = append(replay, model.Event{Type: model.EventConflict, Data: c})
	}
	for _, n := range b.notices {
		replay = append(replay, model.Event{Type: model.EventNotice, Data: model.NoticeEvent{Notice: n, Replay: true}})
	}
	for _, ev := range replay {
		s.push(ev, false)
	}
	if b.final != nil {
		s.push(*b.final, true)
	} else {
		b.subs[s] = struct{}{}
	}
	go s.pump()
	return s.out, func() {
		b.mu.Lock()
		delete(b.subs, s)
		b.mu.Unlock()
		s.stop()
	}
}

// Live returns the latest live status; ok is false before the first.
func (b *Bus) Live() (st model.LiveStatus, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.live != nil {
		st, ok = b.live.Data.(model.LiveStatus)
	}
	return st, ok
}

// Hosts returns copies of all known hosts sorted by IP.
func (b *Bus) Hosts() []model.Host {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hostsLocked()
}

func (b *Bus) hostsLocked() []model.Host {
	out := make([]model.Host, 0, len(b.hosts))
	for _, h := range b.hosts {
		out = append(out, h)
	}
	return report.SortHosts(out)
}

type subscriber struct {
	mu     sync.Mutex
	queue  []model.Event
	closed bool // no more events will be pushed
	signal chan struct{}
	quit   chan struct{}
	once   sync.Once
	out    chan model.Event
}

func newSubscriber() *subscriber {
	return &subscriber{
		signal: make(chan struct{}, 1),
		quit:   make(chan struct{}),
		out:    make(chan model.Event, 16),
	}
}

func (s *subscriber) push(ev model.Event, last bool) {
	s.mu.Lock()
	if !s.closed {
		s.queue = append(s.queue, ev)
		s.closed = last
	}
	s.mu.Unlock()
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

func (s *subscriber) stop() { s.once.Do(func() { close(s.quit) }) }

func (s *subscriber) pump() {
	defer close(s.out)
	for {
		s.mu.Lock()
		batch := s.queue
		s.queue = nil
		closed := s.closed
		s.mu.Unlock()
		for _, ev := range batch {
			select {
			case s.out <- ev:
			case <-s.quit:
				return
			}
		}
		if len(batch) > 0 {
			continue
		}
		if closed {
			return
		}
		select {
		case <-s.signal:
		case <-s.quit:
			return
		}
	}
}

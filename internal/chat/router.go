package chat

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"netdiag/internal/model"
	"netdiag/internal/notify"
	"netdiag/internal/problems"
)

// The alert router (v0.6 4.9 "Router rules"): one per scan, on its event
// bus. Fault notices become posts, OK notices resolves, both through the
// outbox. Alerts go to chat only from a scan that watches the network
// (live mode, v0.6-D10); a resolve goes from any scan, so a card closes
// however its problem was found fixed.

// Watched is the part of *scan.Scan the router uses.
type Watched interface {
	Subscribe() (<-chan model.Event, func())
	Network() string
	Request() model.ScanRequest
	Reconciled() <-chan struct{}
	Done() <-chan struct{}
}

// maxBurst is how many current problems go to chat when sending starts.
const maxBurst = 10

// Attach puts the router on sc's bus until the scan ends. The items a
// previous run left in the outbox go once the scan's first pass is
// reconciled: by then the ledger knows which problems are still open.
func (m *Manager) Attach(sc Watched) {
	ch, unsubscribe := sc.Subscribe()
	live := sc.Request().Live
	m.scansMu.Lock()
	m.scans = append(m.scans, sc)
	m.scansMu.Unlock()
	go func() {
		defer unsubscribe()
		defer m.detach(sc)
		hosts := map[string]model.Host{}
		conflicts := map[string]string{} // ConflictKey -> conflict id
		reconciled := sc.Reconciled()
		for {
			select {
			case <-reconciled:
				reconciled = nil
				m.release()
				m.maybeBurst()
			case ev, ok := <-ch:
				if !ok {
					return
				}
				switch ev.Type {
				case model.EventHost:
					if h, ok := ev.Data.(model.Host); ok {
						hosts[h.IP] = h
					}
				case model.EventConflict:
					if c, ok := ev.Data.(model.ConflictEvent); ok {
						conflicts[notify.ConflictKey(c.Conflict)] = c.Conflict.ID
					}
				case model.EventNotice:
					if n, ok := ev.Data.(model.NoticeEvent); ok && !n.Replay {
						var h *model.Host
						if x, ok := hosts[n.IP]; ok && n.IP != "" {
							h = &x
						}
						m.route(sc.Network(), live, n.Notice, h, conflicts[n.Key])
					}
				}
			}
		}
	}()
}

// detach forgets an ended scan.
func (m *Manager) detach(sc Watched) {
	m.scansMu.Lock()
	defer m.scansMu.Unlock()
	m.scans = slices.DeleteFunc(m.scans, func(x Watched) bool { return x == sc })
}

// release lets the outbox's items from a previous run go.
func (m *Manager) release() {
	if !m.out.holding() {
		return
	}
	var isOpen func(network, key string) bool
	if m.opt.Ledger != nil {
		isOpen = m.opt.Ledger.IsOpen
	}
	m.out.release(isOpen)
}

// route sends one notice of a scan on network. Replayed notices never
// reach it (the bus replays the last 20 to every new subscriber), nor do
// the storm guard's own (local only, v0.6-D16) and the test alert's.
func (m *Manager) route(network string, live bool, n model.Notice, h *model.Host, conflictID string) {
	switch n.Trigger {
	case model.TriggerNetdiagLink, model.TriggerChatTest:
		return
	}
	if n.OK {
		m.routeOK(network, n)
		return
	}
	m.mu.Lock()
	ok := m.conn != nil && m.enabled && live && sends(n.Severity, m.minSeverity)
	m.mu.Unlock()
	if !ok {
		return
	}
	if n.Trigger == model.TriggerNewDevice {
		// Nothing will ever close a new device's card, so it is a note,
		// not an alert.
		m.out.Message(network, "New device on the local network: "+nameAt(n.Name, n.IP)+".")
		return
	}
	update := m.opt.Ledger != nil && m.opt.Ledger.Chat(network, n.Key) == problems.ChatPosted
	m.out.Post(network, n.Key, alertFor(n, h, conflictID), update)
}

// routeOK resolves the card of a fixed problem, whatever its severity and
// whatever Send is set to now, when it was posted or is about to be: the
// ledger keeps that state on the fixed problem too. A problem never sent
// to chat is fixed without a word there.
func (m *Manager) routeOK(network string, n model.Notice) {
	if m.opt.Ledger != nil && m.opt.Ledger.Chat(network, n.Key) == "" {
		return
	}
	m.out.Resolve(network, n.Key, fixedText(n, m.opt.Now()))
}

// droppedProblems resolves the cards of the problems the ledger dropped
// because netdiag has not been on their network for 7 days.
func (m *Manager) droppedProblems(es []problems.Entry) {
	for _, e := range es {
		if e.Chat != "" {
			m.out.Resolve(e.Network, e.Key, "netdiag has not been on this network for 7 days.")
		}
	}
}

// ApplySettings takes the saved settings.chat (at start and after every
// save). Switching Send alerts to team chat off resolves the cards netdiag
// posted, with "netdiag stopped sending alerts here.", and drops what
// waits to be posted; switching it on sends the current problems.
func (m *Manager) ApplySettings(c model.SettingsChat) {
	m.mu.Lock()
	was, first := m.enabled, !m.known
	m.enabled, m.minSeverity, m.known = c.Enabled, c.MinSeverity, true
	switch {
	case first:
	case was && !c.Enabled:
		m.burstDue = false
		m.out.stopPosts()
		if m.opt.Ledger != nil {
			open, _ := m.opt.Ledger.Snapshot()
			for _, e := range open {
				if e.Chat == problems.ChatPosted {
					m.out.Resolve(e.Network, e.Key, "netdiag stopped sending alerts here.")
				}
			}
		}
	case !was && c.Enabled:
		m.burstDue = true
	}
	m.mu.Unlock()
	m.maybeBurst()
}

// watched is the network of the scan watching it now, once its first pass
// is reconciled.
func (m *Manager) watched() (string, bool) {
	m.scansMu.Lock()
	defer m.scansMu.Unlock()
	for i := len(m.scans) - 1; i >= 0; i-- {
		sc := m.scans[i]
		if !sc.Request().Live {
			continue
		}
		select {
		case <-sc.Done():
			continue
		case <-sc.Reconciled():
			return sc.Network(), true
		default:
		}
	}
	return "", false
}

// maybeBurst sends the first-connect message and the current problems
// when that is due and possible (sending on, a channel chosen): one
// message saying netdiag is connected, then at most 10 alerts, the most
// severe first, then how many more there are. Only while netdiag watches
// the network: it posts alerts only then (v0.6-D10). Problems with a card,
// or one on its way, are left out.
func (m *Manager) maybeBurst() {
	m.mu.Lock()
	if !m.burstDue || !m.enabled || m.conn == nil || m.conn.Channel.ID == "" {
		m.mu.Unlock()
		return
	}
	m.burstDue = false
	min := m.minSeverity
	m.mu.Unlock()
	host, _ := m.opt.Hostname()
	who := "netdiag"
	if host = strings.TrimSpace(host); host != "" {
		who = "netdiag on " + host
	}
	network, watching := m.watched()
	m.out.Message(network, who+" is connected. It posts alerts here while it is watching the local network.")
	if !watching || m.opt.Ledger == nil {
		return
	}
	open, _ := m.opt.Ledger.Snapshot()
	var cur []problems.Entry
	for _, e := range open {
		if e.Network == network && e.Chat == "" && sends(e.Severity, min) {
			cur = append(cur, e)
		}
	}
	slices.SortStableFunc(cur, func(a, b problems.Entry) int {
		return cmp.Or(severityRank(b.Severity)-severityRank(a.Severity), strings.Compare(a.OpenedAt, b.OpenedAt))
	})
	for i, e := range cur {
		if i == maxBurst {
			m.out.Message(network, fmt.Sprintf("…and %d more %s. Open %s to see them all.", len(cur)-maxBurst, plural(len(cur)-maxBurst, "problem", "problems"), who))
			break
		}
		m.out.Post(e.Network, e.Key, alertForEntry(e), false)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// moveCards moves the open cards to the new channel after Change channel:
// each is resolved with "Moved to #new." and posted again there.
func (m *Manager) moveCards(old, ch Channel) {
	m.mu.Lock()
	m.oldChannel = old.Name
	m.mu.Unlock()
	if m.opt.Ledger == nil {
		return
	}
	open, _ := m.opt.Ledger.Snapshot()
	for _, e := range open {
		if e.Chat != problems.ChatPosted {
			continue
		}
		m.out.Resolve(e.Network, e.Key, "Moved to #"+ch.Name+".")
		m.out.Post(e.Network, e.Key, alertForEntry(e), false)
	}
}

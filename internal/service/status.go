package service

import (
	"context"

	"netdiag/internal/chat"
	"netdiag/internal/instance"
	"netdiag/internal/model"
	"netdiag/internal/problems"
)

// Status is what MCP get_status reports (v0.6 §4.14): the netdiag window,
// the scan watching the network, the scan running now, a count of the
// problems and team chat. It is a value: it never holds the session token,
// the team chat token or anything to act with, so the MCP layer reads team
// chat and the window through it without importing their packages.
type Status struct {
	Window WindowStatus `json:"window"`
	// Watching is the live-mode scan that is running, nil when nothing
	// watches the network (always nil for a netdiag that is not the
	// window's: it cannot see the window's scans).
	Watching *WatchingStatus `json:"watching"`
	// Running is the scan running now (in its first pass or watching).
	Running  *RunningStatus `json:"running"`
	Problems ProblemsStatus `json:"problems"`
	Chat     ChatStatus     `json:"chat"`
	// Note says in a sentence what this connection can and cannot see.
	Note string `json:"note,omitempty"`
}

// ChatStatus is team chat (bubbles) as get_status reports it: the
// connection's state, where netdiag posts, and what still waits to go. It
// never holds the chat token or its start (the page's tokenPrefix stays in
// GET /api/bubbles). State is the manager's (chat.State*), or ChatUnknown
// for a netdiag that is not the window's: only the window holds the
// connection, so `netdiag mcp` on its own cannot tell.
type ChatStatus struct {
	State string `json:"state"`
	// Reason tells two causes of one state apart (needs_reconnect: refused
	// or unreadable; no_channel: not_allowed or archived).
	Reason string `json:"reason,omitempty"`
	// Since is when the chat server stopped answering (unreachable).
	Since  string      `json:"since,omitempty"`
	Server *ChatServer `json:"server,omitempty"`
	// Channel is the name of the channel alerts go to (without #), the
	// channel bubbles' list_alerts takes; ChannelID is its id.
	Channel   string `json:"channel,omitempty"`
	ChannelID string `json:"channelId,omitempty"`
	// AppName is the chat app netdiag posts as: its alerts' author.
	AppName string `json:"appName,omitempty"`
	// SendAlerts and MinSeverity are the saved Settings > Team chat
	// switch and level. Alerts go only while it is on and a live scan
	// watches the network (D10).
	SendAlerts  bool   `json:"sendAlerts"`
	MinSeverity string `json:"minSeverity,omitempty"`
	// Waiting is what the outbox still has to send (posts, updates and
	// resolves); LastSentAt is when it last delivered something.
	Waiting    int    `json:"waiting"`
	LastSentAt string `json:"lastSentAt,omitempty"`
}

// ChatServer is the chat server netdiag is paired with.
type ChatServer struct {
	Name string `json:"name,omitempty"`
	Host string `json:"host"`
	Port int    `json:"port"`
}

// ChatUnknown is ChatStatus.State for a netdiag that does not hold the
// team chat connection (`netdiag mcp` on its own).
const ChatUnknown = "unknown"

// WindowStatus is the netdiag window (netdiag ui).
type WindowStatus struct {
	Open bool `json:"open"`
	// Port is the window's port on 127.0.0.1 (0 when it is not open).
	Port int `json:"port,omitempty"`
	// AgentAccess is the Settings > Agent access switch (the window's MCP
	// address answers only while it is on).
	AgentAccess bool `json:"agentAccess"`
	// ThisProcess is true when this connection is served by the window
	// itself (its MCP address), so it sees the window's scans.
	ThisProcess bool `json:"thisProcess"`
}

// WatchingStatus is the scan watching the network.
type WatchingStatus struct {
	ScanID    string `json:"scanId"`
	CIDR      string `json:"cidr"`
	Interface string `json:"interface,omitempty"`
	// Since is when it started watching (RFC 3339); "" while its first
	// scan still runs.
	Since       string `json:"since,omitempty"`
	LastCycleAt string `json:"lastCycleAt,omitempty"`
	IntervalSec int    `json:"intervalSec,omitempty"`
	// Held is the storm guard (model.LiveStatus): the checks are paused or
	// the alerts stopped, for HeldReason.
	Held       bool   `json:"held"`
	HeldReason string `json:"heldReason,omitempty"`
}

// RunningStatus is the scan running now.
type RunningStatus struct {
	ScanID    string `json:"scanId"`
	State     string `json:"state"`
	StartedAt string `json:"startedAt"`
}

// ProblemsStatus counts what Problems lists.
type ProblemsStatus struct {
	Open         int    `json:"open"`
	FixedLast24h int    `json:"fixedLast24h"`
	CheckedAt    string `json:"checkedAt,omitempty"`
	// Source is problems.View's: live, latest, saved or none.
	Source         string `json:"source"`
	EverythingFine bool   `json:"everythingFine"`
}

// Status notes.
const (
	NoteNoWindow     = "No netdiag window is open. This connection runs its own netdiag and reads the saved results."
	NoteAgentOff     = "The netdiag window is open but Agent access is off. This connection runs its own netdiag and reads the saved results; the person can turn on Settings > Agent access in the netdiag window."
	NoteOtherProcess = "The netdiag window is open, but this connection runs its own netdiag and cannot see what the window is watching. It reads the saved results; connect to the window's MCP address (Settings > Agent access), or start this connection with netdiag mcp --attach, to see the live view."
	NoteNotWatching  = "netdiag isn't watching the network right now: problems are from the last check. The person can tick Live mode and press Scan in the netdiag window to keep watching."
	NoteNotChecked   = "netdiag has not checked this network yet. The person can tick Live mode and press Scan in the netdiag window to start watching."
)

// SetWindow tells the service it serves the netdiag window, whose port
// port returns (the server calls it once it is made). Without it the
// service is another netdiag (`netdiag mcp` over stdio), and Status looks
// for the window through instance.json.
func (s *Service) SetWindow(port func() int) {
	s.windowMu.Lock()
	s.windowPort = port
	s.windowMu.Unlock()
}

// Status reports the window, the running and watching scans, the problems
// and team chat. Outside the window's netdiag it reads instance.json and
// asks the port there whether that netdiag still serves this data folder
// (with the proof, so another user's netdiag does not count); the token in
// the file never leaves this function.
func (s *Service) Status(ctx context.Context) Status {
	var st Status
	agent := false
	if s.cfg.Store != nil {
		if set, err := s.cfg.Store.LoadSettings(); err == nil {
			agent = set.MCP.Enabled
			st.Chat.SendAlerts, st.Chat.MinSeverity = set.Chat.Enabled, set.Chat.MinSeverity
		}
	}
	s.windowMu.Lock()
	port := s.windowPort
	s.windowMu.Unlock()
	if port != nil {
		st.Window = WindowStatus{Open: true, Port: port(), AgentAccess: agent, ThisProcess: true}
	} else {
		st.Note = NoteNoWindow
		if s.cfg.Store != nil {
			if info, err := instance.Load(s.cfg.Store); err == nil {
				if running, _ := instance.Running(ctx, info); running {
					st.Window = WindowStatus{Open: true, Port: info.Port, AgentAccess: agent}
					st.Note = NoteOtherProcess
					if !agent {
						st.Note = NoteAgentOff
					}
				}
			}
		}
	}

	if s.cfg.Registry != nil {
		if sc, ok := s.cfg.Registry.Running(); ok {
			st.Running = &RunningStatus{ScanID: sc.ID, State: sc.State(), StartedAt: sc.StartedAt()}
			if q := sc.Request(); q.Live {
				w := &WatchingStatus{ScanID: sc.ID, CIDR: q.CIDR, Interface: q.Interface, Since: sc.LiveSince(),
					IntervalSec: q.LiveIntervalSec}
				if ls, ok := sc.LiveStatus(); ok {
					w.LastCycleAt, w.Held, w.HeldReason = ls.LastCheckAt, ls.Held, ls.HeldReason
					if ls.IntervalSec > 0 {
						w.IntervalSec = ls.IntervalSec
					}
				}
				if w.IntervalSec == 0 {
					w.IntervalSec = model.DefaultLiveIntervalSec
				}
				st.Watching = w
			}
		}
	}

	s.chatStatus(&st.Chat, port != nil)

	v := s.Problems()
	st.Problems = ProblemsStatus{Open: len(v.Open), FixedLast24h: len(v.Fixed), CheckedAt: v.CheckedAt,
		Source: v.Source, EverythingFine: v.EverythingFine}
	if port != nil && st.Watching == nil {
		st.Note = NoteNotWatching
	}
	if v.Source == problems.SourceNone && port != nil {
		st.Note = NoteNotChecked
	}
	return st
}

// chatStatus fills c from the team chat manager. The window holds the one
// manager (server.New gives it to the service); a window without one has
// no data folder to keep a connection in, so team chat is off. Any other
// netdiag cannot see the window's connection: unknown.
func (s *Service) chatStatus(c *ChatStatus, window bool) {
	if s.cfg.Chat == nil {
		c.State = ChatUnknown
		if window {
			c.State = chat.StateOff
		}
		return
	}
	cs := s.cfg.Chat.Status()
	c.State, c.Reason, c.Since, c.AppName = cs.State, cs.Reason, cs.Since, cs.AppName
	c.Waiting, c.LastSentAt = cs.Waiting, cs.LastSentAt
	if cs.Server != nil {
		c.Server = &ChatServer{Name: cs.Server.Name, Host: cs.Server.Host, Port: cs.Server.Port}
	}
	if cs.Channel != nil {
		c.Channel, c.ChannelID = cs.Channel.Name, cs.Channel.ID
	}
}

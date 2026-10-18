package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"netdiag/internal/chat"
	"netdiag/internal/model"
	"netdiag/internal/store"
)

// chatToken is the test connection's chat token; get_status must never
// show it, nor the start of it that the page shows (tokenPrefix).
const chatToken = "bub_0123456789abcdefSECRET"

// pairedFiles saves a connection to a chat server on this computer (no
// call is made to it: the manager polls only after an hour) in a new data
// folder, with channel #netdiag-alerts.
func pairedFiles(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := chat.NewConnection(chat.Server{Name: "Front office chat", BaseURL: "https://127.0.0.1:9443", OnboardingURL: "http://127.0.0.1:9080",
		CAFingerprint: "sha256:" + strings.Repeat("ab", 32)}, "netdiag", []string{"alerts:write"}, chatToken, time.Now())
	c.Channel = chat.Channel{ID: "C1", Name: "netdiag-alerts"}
	if err := chat.SaveConnection(st, c); err != nil {
		t.Fatal(err)
	}
	return st
}

func newChatManager(t *testing.T, st *store.Store) *chat.Manager {
	t.Helper()
	m := chat.NewManager(chat.Options{Files: st, PollEvery: time.Hour})
	t.Cleanup(m.Close)
	return m
}

// get_status.chat (M12): the window's team chat manager gives the state,
// server, channel, app and outbox counts, and the saved Send switch; a
// netdiag that is not the window says unknown; a window without a manager
// (no data folder) is off. The token, and its start, never show.
func TestStatusChat(t *testing.T) {
	ctx := context.Background()
	if got := New(Config{}).Status(ctx).Chat; got.State != ChatUnknown || got.Server != nil {
		t.Fatalf("not the window: %+v", got)
	}
	w := New(Config{})
	w.SetWindow(func() int { return 8787 })
	if got := w.Status(ctx).Chat; got.State != chat.StateOff {
		t.Fatalf("window without team chat: %+v", got)
	}

	st := pairedFiles(t)
	set, err := st.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	set.Chat = model.SettingsChat{Enabled: true, MinSeverity: model.ChatSeverityWarning}
	if _, err := st.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	s := New(Config{Store: st, Chat: newChatManager(t, st)})
	s.SetWindow(func() int { return 8787 })
	got := s.Status(ctx)
	c := got.Chat
	if c.State != chat.StateConnected || c.Server == nil || c.Server.Name != "Front office chat" || c.Server.Host != "127.0.0.1" || c.Server.Port != 9443 ||
		c.Channel != "netdiag-alerts" || c.ChannelID != "C1" || c.AppName != "netdiag" || !c.SendAlerts || c.MinSeverity != model.ChatSeverityWarning ||
		c.Waiting != 0 || c.LastSentAt != "" || c.Since != "" {
		t.Fatalf("connected: %+v %+v", c, c.Server)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "bub_") || strings.Contains(string(b), chatToken[:8]) || strings.Contains(strings.ToLower(string(b)), "token") {
		t.Fatalf("status shows the chat token: %s", b)
	}

	// A connection a newer netdiag saved needs reconnecting, and says why.
	data, err := st.ReadFile(store.FileBubbles)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	f["schemaVersion"] = 99
	data, _ = json.Marshal(f)
	if err := st.WriteFile(store.FileBubbles, data); err != nil {
		t.Fatal(err)
	}
	s2 := New(Config{Store: st, Chat: newChatManager(t, st)})
	s2.SetWindow(func() int { return 8787 })
	if c := s2.Status(ctx).Chat; c.State != chat.StateNeedsReconnect || c.Reason != chat.ReasonUnreadable || c.Channel != "netdiag-alerts" {
		t.Fatalf("needs reconnecting: %+v", c)
	}
}

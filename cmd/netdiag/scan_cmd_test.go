package main

import (
	"bytes"
	"strings"
	"testing"

	"netdiag/internal/model"
)

// An OK notice prints a FIXED line with what was fixed and how long the
// problem was open; with --json it is the notice event with ok:true.
func TestLiveWriterFixedLine(t *testing.T) {
	fault := model.Notice{ID: "n-1", Trigger: model.TriggerPrinterOffline, Severity: "warn", IP: "192.168.1.50",
		Title: "Printer offline", Message: "Printer HP (192.168.1.50) stopped responding.",
		At: "2026-10-17T10:30:00Z", Key: "printerOffline|3C:52:82:11:22:33|"}
	ok := model.Notice{ID: "n-2", Trigger: model.TriggerPrinterOffline, Severity: "info", IP: "192.168.1.50",
		Title: "Printer back online", Message: "Printer HP (192.168.1.50) answers again.",
		At: "2026-10-17T10:42:30Z", Key: fault.Key, OK: true, OpenedAt: fault.At}

	var out bytes.Buffer
	l := &liveWriter{stdout: &out, stderr: &out, started: true}
	l.notice(model.NoticeEvent{Notice: fault})
	l.notice(model.NoticeEvent{Notice: ok})
	l.notice(model.NoticeEvent{Notice: ok, Replay: true})
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %q", lines)
	}
	if !strings.Contains(lines[0], "  ALERT  [warn]  Printer offline  192.168.1.50  Printer HP") {
		t.Fatalf("alert line %q", lines[0])
	}
	if !strings.HasSuffix(lines[1], "  FIXED  Printer back online  192.168.1.50  Printer HP (192.168.1.50) answers again.  (lasted 12m)") {
		t.Fatalf("fixed line %q", lines[1])
	}

	// Health checks carry no IP: the message tells them apart.
	out.Reset()
	for _, check := range []string{"DNS", "DHCP"} {
		l.notice(model.NoticeEvent{Notice: model.Notice{ID: "n-" + check, Trigger: model.TriggerHealthFailed, Severity: "info",
			Title: "Health check OK again", Message: check + " check is OK again.", At: "2026-10-17T10:35:00Z",
			Key: "healthFailed||" + strings.ToLower(check), OK: true, OpenedAt: "2026-10-17T10:30:00Z"}})
	}
	lines = strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "  FIXED  Health check OK again  DNS check is OK again.  (lasted 5m)") ||
		!strings.HasSuffix(lines[1], "  FIXED  Health check OK again  DHCP check is OK again.  (lasted 5m)") {
		t.Fatalf("health lines %q", lines)
	}

	out.Reset()
	l.json = true
	l.notice(model.NoticeEvent{Notice: ok})
	if s := out.String(); !strings.Contains(s, `"ok":true`) || !strings.Contains(s, `"openedAt":"2026-10-17T10:30:00Z"`) {
		t.Fatalf("json %s", s)
	}
}

func TestDownFor(t *testing.T) {
	for _, tc := range []struct {
		from, to, want string
		ok             bool
	}{
		{"2026-10-17T10:30:00Z", "2026-10-17T10:30:40Z", "<1m", true},
		{"2026-10-17T10:30:00Z", "2026-10-17T10:42:59Z", "12m", true},
		{"2026-10-17T09:30:00Z", "2026-10-17T10:35:00Z", "1h5m", true},
		{"", "2026-10-17T10:35:00Z", "", false},
		{"2026-10-17T10:35:00Z", "2026-10-17T10:30:00Z", "", false},
	} {
		got, ok := downFor(model.Notice{OpenedAt: tc.from, At: tc.to})
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s..%s = %q %v, want %q %v", tc.from, tc.to, got, ok, tc.want, tc.ok)
		}
	}
}

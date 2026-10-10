package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"netdiag/internal/model"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "health", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseIpconfigAll(t *testing.T) {
	i, err := parseIpconfigAll(fixture(t, "ipconfig_all_en.txt"), "192.168.1.50")
	if err != nil || !i.Enabled || i.APIPA || i.Server != "192.168.1.1" || i.LeaseExpires == "" {
		t.Fatalf("%+v %v", i, err)
	}
	i, err = parseIpconfigAll(fixture(t, "ipconfig_all_en.txt"), "172.20.0.1")
	if err != nil || i.Enabled {
		t.Fatalf("static: %+v %v", i, err)
	}
	i, err = parseIpconfigAll(fixture(t, "ipconfig_all_apipa.txt"), "169.254.12.34")
	if err != nil || !i.APIPA {
		t.Fatalf("apipa: %+v %v", i, err)
	}
	if _, err = parseIpconfigAll(fixture(t, "ipconfig_all_de.txt"), "192.168.178.20"); err == nil {
		t.Fatal("localized sample must be rejected")
	}
	if _, err = parseIpconfigAll(fixture(t, "ipconfig_all_en.txt"), "10.9.9.9"); err == nil {
		t.Fatal("unknown ip must error")
	}
}

func TestParseGetPacket(t *testing.T) {
	i := parseGetPacket(fixture(t, "ipconfig_getpacket.txt"))
	if !i.Enabled || i.Server != "192.168.1.1" || i.LeaseSeconds != 86400 || i.IP != "192.168.1.77" {
		t.Fatalf("%+v", i)
	}
	if parseGetPacket("").Enabled {
		t.Fatal("empty must be static")
	}
}

func TestParseNetsh(t *testing.T) {
	w, err := parseNetshWlan(fixture(t, "netsh_wlan_en.txt"))
	if err != nil || !w.Connected || w.SSID != "EventNet" || w.SignalPct != 87 || w.Channel != 36 ||
		w.Band != "5 GHz" || w.TxRateMbps != 780 || w.BSSID != "11:22:33:44:55:66" {
		t.Fatalf("%+v %v", w, err)
	}
	w, err = parseNetshWlan(fixture(t, "netsh_wlan_disconnected.txt"))
	if err != nil || w.Connected {
		t.Fatalf("%+v %v", w, err)
	}
	if _, err = parseNetshWlan(fixture(t, "netsh_wlan_de.txt")); err == nil {
		t.Fatal("localized sample must be rejected")
	}
}

func TestParseSystemProfiler(t *testing.T) {
	w, err := parseSystemProfiler([]byte(fixture(t, "system_profiler_airport.json")), "en0")
	if err != nil || w.SSID != "EventNet" || w.Channel != 36 || w.Band != "5 GHz" || w.RSSI != -55 || w.SignalPct != 90 || w.TxRateMbps != 866 {
		t.Fatalf("%+v %v", w, err)
	}
	w, err = parseSystemProfiler([]byte(fixture(t, "system_profiler_redacted.json")), "")
	if err != nil || !w.Redacted || w.SSID != "" || !w.Connected {
		t.Fatalf("%+v %v", w, err)
	}
	if _, err = parseSystemProfiler([]byte("{nope"), ""); err == nil {
		t.Fatal("bad json must error")
	}
}

type fakePinger struct {
	ok  bool
	err error
}

func (f fakePinger) Ping(ctx context.Context, ip netip.Addr, to time.Duration) (time.Duration, bool, error) {
	return 3 * time.Millisecond, f.ok, f.err
}

type fakeResolver struct{ err error }

func (f fakeResolver) LookupHost(ctx context.Context, h string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []string{"1.2.3.4"}, nil
}

func newTestRunner(p fakePinger, res fakeResolver, execOut string) *Runner {
	r := &Runner{Pinger: p, Resolver: res, Now: func() time.Time { return time.Unix(1000, 0) },
		Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, errors.New("exec disabled in test")
		}}
	_ = execOut
	r.defaults()
	return r
}

func byID(rep model.HealthReport, id string) model.HealthCheck {
	for _, c := range rep.Checks {
		if c.ID == id {
			return c
		}
	}
	return model.HealthCheck{}
}

func TestRunChecks(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/204" {
			w.WriteHeader(204)
			return
		}
		w.Write([]byte("<html>Event Portal</html>"))
	}))
	defer web.Close()
	r := newTestRunner(fakePinger{ok: true}, fakeResolver{}, "")
	rep := r.Run(context.Background(), Input{Gateway: "192.168.1.1",
		Config: Config{WebURL: web.URL, MustContain: "Event Portal", InternetURL: web.URL + "/204"}})
	if len(rep.Checks) != 6 || rep.CheckedAt == "" {
		t.Fatalf("%+v", rep)
	}
	for _, id := range []string{"gateway", "dns", "webserver", "internet"} {
		if c := byID(rep, id); c.Status != model.HealthOK {
			t.Errorf("%s: %+v", id, c)
		}
	}
	if c := byID(rep, "wifi"); c.Status != model.HealthUnavailable {
		t.Errorf("wifi: %+v", c)
	}
	// failures
	r = newTestRunner(fakePinger{ok: false}, fakeResolver{err: errors.New("nxdomain")}, "")
	rep = r.Run(context.Background(), Input{Gateway: "192.168.1.1",
		Config: Config{WebURL: web.URL, MustContain: "missing", ExpectStatus: 200, InternetURL: "http://127.0.0.1:1/"}})
	if byID(rep, "gateway").Status != model.HealthFail || byID(rep, "dns").Status != model.HealthFail ||
		byID(rep, "webserver").Status != model.HealthFail || byID(rep, "internet").Status != model.HealthInfo {
		t.Errorf("%+v", rep.Checks)
	}
	rep = r.Run(context.Background(), Input{Config: Config{ExpectStatus: 200}})
	if byID(rep, "gateway").Status != model.HealthUnavailable || byID(rep, "webserver").Status != model.HealthInfo {
		t.Errorf("%+v", rep.Checks)
	}
}

func TestWiFiCache(t *testing.T) {
	r := newTestRunner(fakePinger{}, fakeResolver{}, "")
	calls := 0
	r.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls++
		return nil, errors.New("x")
	}
	for i := 0; i < 3; i++ {
		r.checkWiFi(context.Background(), Input{Iface: "en0"})
	}
	if calls > 1 {
		t.Fatalf("expected cached wifi, exec calls=%d", calls)
	}
}

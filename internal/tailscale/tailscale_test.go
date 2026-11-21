package tailscale

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const statusJSON = `{
  "Version": "1.90.0",
  "BackendState": "Running",
  "MagicDNSSuffix": "tail1234.ts.net",
  "Health": ["", "Some peers are advertising routes but --accept-routes is false"],
  "CurrentTailnet": {"Name": "example.com", "MagicDNSSuffix": "tail1234.ts.net", "MagicDNSEnabled": true},
  "Self": {"ID": "s1", "HostName": "DESKTOP", "DNSName": "desktop.tail1234.ts.net.", "OS": "windows",
           "TailscaleIPs": ["100.101.102.103", "fd7a:115c:a1e0::1"], "Online": true, "Relay": "nyc",
           "KeyExpiry": "2027-05-20T20:21:23Z"},
  "Peer": {
    "k1": {"ID": "p1", "HostName": "nas", "DNSName": "nas.tail1234.ts.net.", "OS": "linux",
           "TailscaleIPs": ["100.64.12.34"], "Online": true, "Active": true, "Relay": "nyc",
           "CurAddr": "192.168.1.50:41641", "LastSeen": "0001-01-01T00:00:00Z", "PrimaryRoutes": ["192.168.10.0/24"]},
    "k2": {"ID": "p2", "HostName": "phone", "DNSName": "phone.tail1234.ts.net.", "OS": "android",
           "TailscaleIPs": ["100.64.0.20"], "Online": false, "Relay": "nyc", "LastSeen": "2026-11-18T15:19:33Z"},
    "k3": {"ID": "p3", "HostName": "cloud-vm", "DNSName": "cloud-vm.tail1234.ts.net.", "OS": "linux",
           "TailscaleIPs": ["100.70.1.2"], "Online": true, "Active": true, "Relay": "fra", "ExitNodeOption": true, "ExitNode": true,
           "Tags": ["tag:server"]},
    "k4": {"ID": "p4", "HostName": "", "DNSName": "idle-box.tail1234.ts.net.", "OS": "macOS",
           "TailscaleIPs": ["100.70.1.3"], "Online": true, "Active": false, "Relay": "nyc"}
  }
}`

// fakeService answers one LocalAPI request per connection, like tailscaled.
type fakeService struct {
	t       *testing.T
	answer  func(r *http.Request) (int, string)
	token   string
	gotAuth string
	paths   []string
}

func (f *fakeService) dial(context.Context) (io.ReadWriteCloser, string, error) {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		req, err := http.ReadRequest(bufio.NewReader(server))
		if err != nil {
			return
		}
		f.paths = append(f.paths, req.Method+" "+req.URL.RequestURI())
		_, f.gotAuth, _ = req.BasicAuth()
		code, body := f.answer(req)
		resp := &http.Response{StatusCode: code, ProtoMajor: 1, ProtoMinor: 1, Body: io.NopCloser(strings.NewReader(body)),
			ContentLength: int64(len(body)), Header: http.Header{}}
		_ = resp.Write(server)
	}()
	return client, f.token, nil
}

func TestStatus(t *testing.T) {
	f := &fakeService{t: t, token: "tok", answer: func(*http.Request) (int, string) { return 200, statusJSON }}
	c := &Client{dial: f.dial, now: func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) }}
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.paths[0] != "GET /localapi/v0/status" || f.gotAuth != "tok" {
		t.Fatalf("request %v auth %q", f.paths, f.gotAuth)
	}
	if st.State != StateRunning || st.Tailnet != "example.com" || !st.MagicDNS || st.Suffix != "tail1234.ts.net" || st.Version != "1.90.0" {
		t.Fatalf("status %+v", st)
	}
	if len(st.Health) != 1 || !strings.Contains(st.Health[0], "accept-routes") {
		t.Fatalf("health %q", st.Health)
	}
	if st.Self == nil || st.Self.Name != "DESKTOP" || st.Self.DNSName != "desktop.tail1234.ts.net" || st.Self.KeyExpiry.IsZero() || len(st.Self.IPs) != 2 {
		t.Fatalf("self %+v", st.Self)
	}
	names := []string{}
	for _, d := range st.Devices {
		names = append(names, d.Name+":"+d.Path)
	}
	// Online first, then by name; the nameless one takes its MagicDNS label.
	if strings.Join(names, ",") != "cloud-vm:relayed,idle-box:idle,nas:direct,phone:offline" {
		t.Fatalf("devices %v", names)
	}
	byName := map[string]Device{}
	for _, d := range st.Devices {
		byName[d.Name] = d
	}
	if d := byName["nas"]; d.Endpoint != "192.168.1.50:41641" || d.Relay != "" || !d.LastSeen.IsZero() || d.SubnetRoutes[0] != "192.168.10.0/24" {
		t.Fatalf("nas %+v", d)
	}
	if d := byName["cloud-vm"]; d.Relay != "fra" || !d.ExitNode || !d.ExitNodeOption || d.Tags[0] != "tag:server" {
		t.Fatalf("cloud-vm %+v", d)
	}
	if d := byName["phone"]; d.Relay != "" || d.LastSeen.IsZero() || d.Online {
		t.Fatalf("phone %+v", d)
	}
	if !st.CheckedAt.Equal(time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("checkedAt %v", st.CheckedAt)
	}
}

func TestStatusStates(t *testing.T) {
	for backend, want := range map[string]string{"Stopped": StateStopped, "NeedsLogin": StateNeedsLogin, "NeedsMachineAuth": StateNeedsLogin, "Starting": StateStopped} {
		f := &fakeService{t: t, answer: func(*http.Request) (int, string) { return 200, `{"BackendState":"` + backend + `"}` }}
		st, err := (&Client{dial: f.dial}).Status(context.Background())
		if err != nil || st.State != want || st.Devices == nil || st.Health == nil {
			t.Errorf("%s: %+v %v", backend, st, err)
		}
	}
	notThere := &Client{dial: func(context.Context) (io.ReadWriteCloser, string, error) { return nil, "", ErrNotInstalled }}
	st, err := notThere.Status(context.Background())
	if err != nil || st.State != StateNotInstalled {
		t.Fatalf("not installed: %+v %v", st, err)
	}
	refused := &fakeService{t: t, answer: func(*http.Request) (int, string) { return 403, "access denied" }}
	if _, err := (&Client{dial: refused.dial}).Status(context.Background()); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("refused: %v", err)
	}
	garbage := &fakeService{t: t, answer: func(*http.Request) (int, string) { return 200, "not json" }}
	if _, err := (&Client{dial: garbage.dial}).Status(context.Background()); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestStatusTimesOut(t *testing.T) {
	hang := &Client{dial: func(context.Context) (io.ReadWriteCloser, string, error) {
		client, server := net.Pipe()
		go func() { _, _ = io.Copy(io.Discard, server) }() // never answers
		return client, "", nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := hang.Status(ctx); err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("hang: %v after %v", err, time.Since(start))
	}
}

func TestPing(t *testing.T) {
	f := &fakeService{t: t, answer: func(r *http.Request) (int, string) {
		if r.URL.Query().Get("ip") == "100.70.1.2" {
			return 200, `{"IP":"100.70.1.2","NodeName":"cloud-vm","LatencySeconds":0.0421,"DERPRegionCode":"fra"}`
		}
		if r.URL.Query().Get("ip") == "100.64.0.20" {
			return 200, `{"Err":"timeout waiting for ping reply"}`
		}
		return 200, `{"IP":"100.64.12.34","NodeName":"nas","LatencySeconds":0.0011,"Endpoint":"192.168.1.50:41641"}`
	}}
	c := &Client{dial: f.dial}
	res, err := c.Ping(context.Background(), "100.64.12.34")
	if err != nil || !res.OK || res.Path != PathDirect || res.Endpoint != "192.168.1.50:41641" || res.Latency < 1 || res.Latency > 1.2 {
		t.Fatalf("direct: %+v %v", res, err)
	}
	if f.paths[0] != "POST /localapi/v0/ping?ip=100.64.12.34&type=disco" {
		t.Fatalf("request %v", f.paths)
	}
	if res, _ := c.Ping(context.Background(), "100.70.1.2"); res.Path != PathRelayed || res.Relay != "fra" || res.Name != "cloud-vm" {
		t.Fatalf("relayed: %+v", res)
	}
	if res, _ := c.Ping(context.Background(), "100.64.0.20"); res.OK || res.Error == "" {
		t.Fatalf("timeout: %+v", res)
	}
	for _, bad := range []string{"192.168.1.1", "8.8.8.8", "not-an-ip", "100.128.0.1", "fd00::1"} {
		if _, err := c.Ping(context.Background(), bad); err == nil {
			t.Errorf("Ping(%q) accepted", bad)
		}
	}
	if _, err := c.Ping(context.Background(), "fd7a:115c:a1e0::1"); err != nil {
		t.Errorf("Tailscale IPv6 refused: %v", err)
	}
}

// TestLiveStatus reads this computer's real Tailscale service; run it with
// NETDIAG_TAILSCALE_LIVE=1 on a computer with Tailscale.
func TestLiveStatus(t *testing.T) {
	if os.Getenv("NETDIAG_TAILSCALE_LIVE") != "1" {
		t.Skip("set NETDIAG_TAILSCALE_LIVE=1 to read the real Tailscale service")
	}
	st, err := New().Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("state=%s tailnet=%s self=%+v", st.State, st.Tailnet, st.Self)
	for _, d := range st.Devices {
		t.Logf("device %s os=%s path=%s endpoint=%s relay=%s ips=%v", d.Name, d.OS, d.Path, d.Endpoint, d.Relay, d.IPs)
		if d.Online && len(d.IPs) > 0 {
			res, err := New().Ping(context.Background(), d.IPs[0])
			t.Logf("  ping %+v %v", res, err)
		}
	}
	if st.State == StateNotInstalled {
		t.Fatal("Tailscale not found")
	}
	_ = errors.New
}

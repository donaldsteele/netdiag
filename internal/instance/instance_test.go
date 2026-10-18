package instance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"netdiag/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// The token is 32 random bytes in base64url without padding, new every time;
// the instance id is "i-" and 32 hex digits.
func TestTokenEntropyAndFormat(t *testing.T) {
	seen := map[string]bool{}
	re := regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	for range 200 {
		tok := NewToken()
		if !re.MatchString(tok) {
			t.Fatalf("token %q is not 43 base64url characters", tok)
		}
		b, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil || len(b) != 32 {
			t.Fatalf("token %q decodes to %d bytes (%v)", tok, len(b), err)
		}
		if seen[tok] {
			t.Fatalf("token %q repeated", tok)
		}
		seen[tok] = true
	}
	if id := NewID(); !regexp.MustCompile(`^i-[0-9a-f]{32}$`).MatchString(id) || id == NewID() {
		t.Fatalf("instance id %q", id)
	}
	if u := PageURL(8787, "abc"); u != "http://127.0.0.1:8787/#s=abc" {
		t.Fatalf("page url %q", u)
	}
}

// instance.json round-trips with schemaVersion 1; a missing file is the zero
// Info; a file that is not JSON is set aside and reads as missing.
func TestLoadSave(t *testing.T) {
	st := openStore(t)
	if i, err := Load(st); err != nil || i != (Info{}) {
		t.Fatalf("missing file: %+v %v", i, err)
	}
	want := Info{Port: 8787, PID: 42, InstanceID: "i-1", Token: NewToken(), StartedAt: "2026-10-17T09:00:00Z", Version: "v0.6.0"}
	if err := Save(st, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(st)
	want.SchemaVersion = 1
	if err != nil || got != want {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	raw, _ := os.ReadFile(filepath.Join(st.Dir(), store.FileInstance))
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"schemaVersion", "port", "pid", "instanceId", "token", "startedAt", "version"} {
		if _, ok := m[k]; !ok {
			t.Errorf("instance.json lacks %q: %s", k, raw)
		}
	}
	// Printing an Info never shows the token.
	for _, s := range []string{fmt.Sprint(got), fmt.Sprintf("%v %+v %#v %s", got, got, got, got)} {
		if strings.Contains(s, got.Token) {
			t.Fatalf("printed token: %s", s)
		}
	}

	if err := os.WriteFile(filepath.Join(st.Dir(), store.FileInstance), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if i, err := Load(st); err != nil || i != (Info{}) {
		t.Fatalf("bad file: %+v %v", i, err)
	}
	ents, _ := os.ReadDir(st.Dir())
	bad := false
	for _, e := range ents {
		bad = bad || strings.HasPrefix(e.Name(), store.FileInstance+".bad-")
	}
	if !bad {
		t.Fatal("bad file was not set aside")
	}
}

// identityServer answers GET /api/identity on 127.0.0.1 with id.
func identityServer(t *testing.T, id Identity) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/identity" || r.Host != ln.Addr().String() {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(id)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// freePort returns a port on 127.0.0.1 that nothing listens on.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return p
}

// The identity decision: the same instanceId on the remembered port means
// that netdiag is running (reuse it); another netdiag there is foreign
// (pick a random port); nothing there, or something that is not netdiag,
// is a stale file (try the remembered port, then 8787, then random).
func TestIdentityDecision(t *testing.T) {
	ctx := context.Background()
	mine := Info{InstanceID: "i-mine", Token: "t"}

	mine.Port = identityServer(t, Identity{App: "netdiag", InstanceID: "i-mine", PID: 7})
	if run, id, ans := Running(ctx, mine); !run || !ans || id.PID != 7 {
		t.Fatalf("same instance: %v %+v %v", run, id, ans)
	}

	foreign := mine
	foreign.Port = identityServer(t, Identity{App: "netdiag", InstanceID: "i-other"})
	run, id, ans := Running(ctx, foreign)
	if run || !ans || !foreign.Foreign(id) {
		t.Fatalf("foreign: %v %+v %v", run, id, ans)
	}
	if got := Ports(0, foreign.Port, true); !slices.Equal(got, []int{0}) {
		t.Fatalf("foreign ports %v", got)
	}

	other := mine
	other.Port = identityServer(t, Identity{App: "something-else", InstanceID: "i-mine"})
	run, id, ans = Running(ctx, other)
	if run || !ans || other.Foreign(id) {
		t.Fatalf("another app: %v %+v %v", run, id, ans)
	}

	stale := mine
	stale.Port = freePort(t)
	if run, _, ans := Running(ctx, stale); run || ans {
		t.Fatalf("stale: %v %v", run, ans)
	}
	if got := Ports(0, stale.Port, false); !slices.Equal(got, []int{stale.Port, DefaultPort, 0}) {
		t.Fatalf("stale ports %v", got)
	}
	if run, _, ans := Running(ctx, Info{InstanceID: "i-mine"}); run || ans {
		t.Fatal("no port should not be probed")
	}
	// An empty instance id never matches, even an empty answer.
	if (Info{}).Matches(Identity{App: "netdiag"}) {
		t.Fatal("empty instance id matched")
	}
}

func TestPortOrder(t *testing.T) {
	for _, c := range []struct {
		flag, remembered int
		foreign          bool
		want             []int
	}{
		{0, 0, false, []int{DefaultPort, 0}},
		{0, DefaultPort, false, []int{DefaultPort, 0}},
		{0, 50123, false, []int{50123, DefaultPort, 0}},
		{0, 50123, true, []int{0}},
		{9000, 50123, true, []int{9000}},
		{9000, 0, false, []int{9000}},
		{0, 443, false, []int{DefaultPort, 0}},
	} {
		if got := Ports(c.flag, c.remembered, c.foreign); !slices.Equal(got, c.want) {
			t.Errorf("Ports(%d, %d, %v) = %v, want %v", c.flag, c.remembered, c.foreign, got, c.want)
		}
	}
}

// The probe gives up after its timeout on a port that accepts but never
// answers.
func TestProbeTimeout(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() // the system accepts the connection; nobody reads it
	port, _ := strconv.Atoi(strings.TrimPrefix(ln.Addr().String(), "127.0.0.1:"))
	t0 := time.Now()
	if _, err := Probe(context.Background(), port, 200*time.Millisecond); err == nil {
		t.Fatal("no answer should fail")
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("probe took %v", d)
	}
}

// One start lock per data folder: a second Acquire fails until the first
// lets go; another folder has its own lock; Owner reports the holder.
func TestLockPerDataFolder(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	la, err := Acquire(a)
	if err != nil {
		t.Fatal(err)
	}
	if !Owner(a) || Owner(b) {
		t.Fatalf("owner: %v %v", Owner(a), Owner(b))
	}
	if _, err := Acquire(a); err != ErrLocked {
		t.Fatalf("second acquire: %v", err)
	}
	if _, err := Acquire(filepath.Join(a, ".")); err != ErrLocked {
		t.Fatalf("same folder spelled differently: %v", err)
	}
	lb, err := Acquire(b)
	if err != nil {
		t.Fatalf("other folder: %v", err)
	}
	lb.Release()
	la.Release()
	la.Release()
	if Owner(a) {
		t.Fatal("still owner after release")
	}
	again, err := Acquire(a)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again.Release()
}

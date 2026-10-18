package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"netdiag/internal/instance"
	"netdiag/internal/store"
)

// syncBuf is a bytes.Buffer that runUI in another goroutine can write to.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// browserSpy replaces openBrowser for one test and records the URLs.
func browserSpy(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var urls []string
	old := openBrowser
	openBrowser = func(u string) error {
		mu.Lock()
		defer mu.Unlock()
		urls = append(urls, u)
		return nil
	}
	t.Cleanup(func() { openBrowser = old })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), urls...)
	}
}

// dataDir points NETDIAG_DATA_DIR at a new folder, so a test never uses
// (or collides with) the data folder of a netdiag the person runs.
func dataDir(t *testing.T) (string, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(store.EnvDataDir, dir)
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, st
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// waitInstance waits until instance.json names a netdiag other than not.
func waitInstance(t *testing.T, st *store.Store, not string) instance.Info {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if i, err := instance.Load(st); err == nil && i.Port != 0 && i.InstanceID != not {
			return i
		}
		if time.Now().After(deadline) {
			t.Fatal("instance.json was not written")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// post sends a JSON POST with token in X-Netdiag-Session ("" sends none).
func post(t *testing.T, url, token string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Netdiag-Session", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// startUI runs runUI in the background and returns its exit code channel.
func startUI(t *testing.T, args ...string) (chan int, *syncBuf) {
	t.Helper()
	out := &syncBuf{}
	code := make(chan int, 1)
	go func() { code <- runUI(args, out, out) }()
	return code, out
}

// quitUI stops a runUI started by the test with the token from
// instance.json, and checks that it exits 0.
func quitUI(t *testing.T, info instance.Info, code chan int) {
	t.Helper()
	if c := post(t, "http://127.0.0.1:"+strconv.Itoa(info.Port)+"/api/quit", info.Token); c != 204 {
		t.Fatalf("quit with the token: %d", c)
	}
	select {
	case c := <-code:
		if c != 0 {
			t.Fatalf("first netdiag exited %d", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("netdiag did not quit")
	}
}

// A second launch with the same data folder opens the running netdiag's
// page, with its session token, and exits 0. POST /api/quit without the
// header gets 401; with the token from instance.json, 204.
func TestSecondLaunchOpensSamePage(t *testing.T) {
	_, st := dataDir(t)
	opened := browserSpy(t)
	p := freePort(t)
	code, out := startUI(t, "--port", strconv.Itoa(p))
	info := waitInstance(t, st, "")
	if info.Port != p || info.PID != os.Getpid() || info.Token == "" || !strings.HasPrefix(info.InstanceID, "i-") || info.Version != version {
		t.Fatalf("instance.json = %v (port %d)", info, info.Port)
	}
	if got := opened(); len(got) != 1 || got[0] != instance.PageURL(p, info.Token) {
		t.Fatalf("first launch opened %v", got)
	}
	if strings.Contains(out.String(), info.Token) {
		t.Fatalf("the token was printed: %s", out)
	}
	// The page answers identity with the id in the file and, for a nonce, a
	// proof made with the token in the file.
	nonce := instance.NewNonce()
	id, err := instance.Probe(context.Background(), p, nonce, 2*time.Second)
	if err != nil || !info.Matches(id) || !info.Proves(nonce, id.Proof) {
		t.Fatalf("identity %+v %v", id, err)
	}

	var second bytes.Buffer
	if c := runUI(nil, &second, &second); c != 0 {
		t.Fatalf("second launch exited %d: %s", c, second.String())
	}
	want := "netdiag is already running at http://127.0.0.1:" + strconv.Itoa(p) + ". Opened it in your browser."
	if !strings.Contains(second.String(), want) || strings.Contains(second.String(), info.Token) {
		t.Fatalf("second launch said %q", second.String())
	}
	if got := opened(); len(got) != 2 || got[1] != instance.PageURL(p, info.Token) {
		t.Fatalf("second launch opened %v", got)
	}
	// Asking for another port changes nothing: one netdiag per data folder.
	// With --no-browser it says how to get a page whose buttons work.
	second.Reset()
	if c := runUI([]string{"--port", strconv.Itoa(freePort(t)), "--no-browser"}, &second, &second); c != 0 ||
		!strings.Contains(second.String(), "already running at http://127.0.0.1:"+strconv.Itoa(p)+".") ||
		!strings.Contains(second.String(), openAgain) || strings.Contains(second.String(), info.Token) {
		t.Fatalf("second launch with --port: %d %q", c, second.String())
	}
	if got := opened(); len(got) != 2 {
		t.Fatalf("--no-browser opened %v", got)
	}
	if again, _ := instance.Load(st); again != info {
		t.Fatalf("the second launch rewrote instance.json: %v", again)
	}

	if c := post(t, "http://127.0.0.1:"+strconv.Itoa(p)+"/api/quit", ""); c != 401 {
		t.Fatalf("quit without the header: %d", c)
	}
	quitUI(t, info, code)
}

// identityServer answers GET /api/identity on 127.0.0.1 as another netdiag
// with instanceID, and no proof.
func identityServer(t *testing.T, instanceID string) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(instance.Identity{App: "netdiag", InstanceID: instanceID, PID: 1})
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// Another netdiag (another OS user's, or one with another data folder) on
// the remembered port: this one picks a random port, not 8787, and
// remembers it.
func TestForeignInstanceOnRememberedPort(t *testing.T) {
	_, st := dataDir(t)
	browserSpy(t)
	theirs := identityServer(t, "i-theirs")
	if err := instance.Save(st, instance.Info{Port: theirs, InstanceID: "i-mine", Token: "old"}); err != nil {
		t.Fatal(err)
	}
	code, out := startUI(t, "--no-browser")
	info := waitInstance(t, st, "i-mine")
	if info.Port == theirs || info.Port == instance.DefaultPort || info.Token == "old" {
		t.Fatalf("picked port %d (theirs %d), token %q", info.Port, theirs, info.Token)
	}
	// The address alone opens a page whose buttons are off: the console
	// says how to get one that works, without printing the token.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), openAgain) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if s := out.String(); !strings.Contains(s, openAgain) || strings.Contains(s, info.Token) {
		t.Fatalf("--no-browser said %q", s)
	}
	quitUI(t, info, code)
}

// Another OS user read this netdiag's instance id from GET /api/identity
// (it needs no token), took the remembered port after netdiag quit, and
// answers with that id. It cannot prove the token in instance.json, so a
// launch neither opens its page with the old token nor stops: it starts
// its own netdiag on a random port (v0.6-D33, plan 7.5).
func TestSpoofedIdentityOnRememberedPort(t *testing.T) {
	_, st := dataDir(t)
	opened := browserSpy(t)
	theirs := identityServer(t, "i-mine")
	if err := instance.Save(st, instance.Info{Port: theirs, InstanceID: "i-mine", Token: "old"}); err != nil {
		t.Fatal(err)
	}
	code, out := startUI(t)
	info := waitInstance(t, st, "i-mine")
	if info.Port == theirs || info.Port == instance.DefaultPort || info.Token == "old" {
		t.Fatalf("picked port %d (theirs %d), token %q", info.Port, theirs, info.Token)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(opened()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := opened(); len(got) != 1 || got[0] != instance.PageURL(info.Port, info.Token) {
		t.Fatalf("opened %v, want this netdiag's own page", got)
	}
	if strings.Contains(out.String(), "already running") {
		t.Fatalf("trusted the spoofed identity: %q", out.String())
	}
	quitUI(t, info, code)
}

// A failed write of instance.json is tried again: on Windows replacing the
// file fails while a second launch (or a virus scanner) has it open, which
// is exactly when a double-click races this one. Once the reader lets go,
// the write goes through.
func TestSaveInstanceRetries(t *testing.T) {
	_, st := dataDir(t)
	if err := instance.Save(st, instance.Info{Port: 1, InstanceID: "i-old", Token: "old"}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(st.Dir(), store.FileInstance))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if err := instance.Save(st, instance.Info{Port: 2, InstanceID: "i-probe", Token: "x"}); err == nil {
			t.Log("this system replaced instance.json while it was open")
		}
	}
	go func() {
		time.Sleep(60 * time.Millisecond)
		f.Close()
	}()
	want := instance.Info{Port: 3, PID: 7, InstanceID: "i-new", Token: "new", Version: version}
	if err := saveInstance(st, want); err != nil {
		t.Fatalf("save after the reader let go: %v", err)
	}
	want.SchemaVersion = 1
	if got, err := instance.Load(st); err != nil || got != want {
		t.Fatalf("instance.json = %v %v", got, err)
	}
}

// When instance.json cannot be written at all, netdiag still serves the page
// it opened (with its token), keeps the start lock so no second netdiag
// serves the folder, and says that opening it again will not find it.
func TestSaveInstanceFails(t *testing.T) {
	_, st := dataDir(t)
	opened := browserSpy(t)
	var tries int
	var mu sync.Mutex
	oldSave, oldDelays := saveInfo, saveDelays
	saveInfo = func(*store.Store, instance.Info) error {
		mu.Lock()
		defer mu.Unlock()
		tries++
		return os.ErrPermission
	}
	saveDelays = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { saveInfo, saveDelays = oldSave, oldDelays })
	p := freePort(t)
	code, out := startUI(t, "--port", strconv.Itoa(p))
	deadline := time.Now().Add(10 * time.Second)
	for len(opened()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	got := opened()
	if len(got) != 1 || !strings.HasPrefix(got[0], instance.PageURL(p, "")) {
		t.Fatalf("opened %v", got)
	}
	mu.Lock()
	n := tries
	mu.Unlock()
	if n != 3 || !strings.Contains(out.String(), "could not save instance.json") ||
		!strings.Contains(out.String(), "opening it again will not find this window until you quit it") {
		t.Fatalf("%d tries; said %q", n, out.String())
	}
	if !instance.Owner(st.Dir()) {
		t.Fatal("netdiag let go of the start lock")
	}
	quitUI(t, instance.Info{Port: p, Token: strings.TrimPrefix(got[0], instance.PageURL(p, ""))}, code)
}

// A stale instance.json (nothing answers on its port) is the remembered
// address: netdiag starts there again with a new token and instance id.
func TestStaleInstanceFileReusesPort(t *testing.T) {
	_, st := dataDir(t)
	browserSpy(t)
	p := freePort(t)
	if err := instance.Save(st, instance.Info{Port: p, PID: 1, InstanceID: "i-old", Token: "old"}); err != nil {
		t.Fatal(err)
	}
	code, _ := startUI(t, "--no-browser")
	info := waitInstance(t, st, "i-old")
	if info.Port != p || info.Token == "old" {
		t.Fatalf("stale file: port %d (want %d), token %q", info.Port, p, info.Token)
	}
	quitUI(t, info, code)
}

// Another netdiag holds the start lock for this data folder and never
// answers: after waiting, the launch says so and exits 1 without serving.
func TestLockedDataFolderNotAnswering(t *testing.T) {
	dir, st := dataDir(t)
	browserSpy(t)
	lock, err := instance.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	old := lockWait
	lockWait = 300 * time.Millisecond
	t.Cleanup(func() { lockWait = old })
	var out bytes.Buffer
	if c := runUI([]string{"--no-browser"}, &out, &out); c != 1 || !strings.Contains(out.String(), "another netdiag is using the data folder") {
		t.Fatalf("locked: %d %q", c, out.String())
	}
	if i, _ := instance.Load(st); i != (instance.Info{}) {
		t.Fatalf("instance.json written while locked: %v", i)
	}
}

// The real binary, twice, as a double-click would start it: the second
// process finds the first through instance.json and the start lock, says
// where it is and exits 0. Both use a new data folder and a free port.
func TestSecondProcessExitsZero(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "netdiag")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if o, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, o)
	}
	data := filepath.Join(dir, "data")
	env := append(os.Environ(), store.EnvDataDir+"="+data)
	p := freePort(t)
	first := exec.Command(bin, "ui", "--no-browser", "--port", strconv.Itoa(p))
	first.Env = env
	// A file, not a pipe: Wait then waits for the process only.
	logf, err := os.Create(filepath.Join(dir, "first.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close()
	first.Stdout, first.Stderr = logf, logf
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- first.Wait() }()
	quit := false
	// Only the process this test started is stopped, by its handle.
	defer func() {
		if !quit {
			_ = first.Process.Kill()
			<-exited
		}
	}()
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	info := waitInstance(t, st, "")
	if info.PID != first.Process.Pid || info.Port != p {
		t.Fatalf("instance.json = %v, pid %d", info, first.Process.Pid)
	}

	second := exec.Command(bin, "ui", "--no-browser")
	second.Env = env
	out, err := second.CombinedOutput()
	if err != nil {
		t.Fatalf("second launch: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "netdiag is already running at http://127.0.0.1:"+strconv.Itoa(p)+".") {
		t.Fatalf("second launch said %q", out)
	}

	url := "http://127.0.0.1:" + strconv.Itoa(p) + "/api/quit"
	if c := post(t, url, ""); c != 401 {
		t.Fatalf("quit without the header: %d", c)
	}
	if c := post(t, url, info.Token); c != 204 {
		t.Fatalf("quit with the header from instance.json: %d", c)
	}
	select {
	case err := <-exited:
		quit = true
		if err != nil {
			log, _ := os.ReadFile(logf.Name())
			t.Fatalf("first netdiag: %v\n%s", err, log)
		}
	case <-time.After(15 * time.Second):
		log, _ := os.ReadFile(logf.Name())
		t.Fatalf("first netdiag did not quit\n%s", log)
	}
}

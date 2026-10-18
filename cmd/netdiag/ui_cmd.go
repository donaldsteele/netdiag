package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/pkg/browser"

	"netdiag/internal/instance"
	"netdiag/internal/problems"
	"netdiag/internal/scan"
	"netdiag/internal/server"
	"netdiag/internal/store"
	"netdiag/web"
)

// openBrowser opens a page in the default browser; tests replace it.
var openBrowser = browser.OpenURL

// lockWait is how long a launch waits for the netdiag that holds the start
// lock to answer before it gives up.
var lockWait = 3 * time.Second

// runUI implements `netdiag ui` (and the no-argument default).
//
// One netdiag serves a data folder (v0.6 F9b). A launch first asks the port
// in instance.json who is there: when the same netdiag answers, it opens
// that page (with its session token) and exits 0. Otherwise it takes the
// start lock, picks a port (--port, then the remembered one, then 8787, then
// a random one; D6) and writes instance.json with a new token.
func runUI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	port := fs.Int("port", 0, "port to listen on (default: the port netdiag used last, else 8787, else a random free port)")
	noBrowser := fs.Bool("no-browser", false, "do not open the default browser")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 || *port < 0 || *port > 65535 || *port == 80 || *port == 443 {
		fmt.Fprintln(stderr, "error: invalid arguments")
		return 2
	}
	st, err := store.Open("")
	if err != nil {
		fmt.Fprintln(stderr, "warning: settings/history unavailable:", err)
		st = nil
	}
	var (
		remembered int
		foreign    bool
	)
	if st != nil {
		info, lock, answer, code := claim(st, *noBrowser, stdout, stderr)
		if lock == nil && code >= 0 {
			return code
		}
		defer lock.Release()
		remembered, foreign = info.Port, answer
	}
	// One open-problem ledger and one network profile store per process,
	// shared by every scan and the service, so each file has one writer.
	// They open after the start lock, so a second launch never reads them.
	deps := scan.DefaultDeps()
	var ledger *problems.Ledger
	if st != nil {
		if ledger, err = problems.OpenLedger(st); err != nil {
			fmt.Fprintln(stderr, "warning: open problems:", err)
		}
		deps.Ledger = ledger
		deps.Profiles = scan.NewProfiles(st)
	}
	token, id := instance.NewToken(), instance.NewID()
	var logMu sync.Mutex // team chat logs from its own goroutines
	srv := server.New(server.Config{
		Registry:     scan.NewRegistry(scan.New(deps)),
		Static:       web.FS(),
		Store:        st,
		Version:      version,
		InstanceID:   id,
		SessionToken: token,
		Ledger:       ledger,
		Logf: func(format string, args ...any) {
			logMu.Lock()
			defer logMu.Unlock()
			fmt.Fprintf(stderr, format+"\n", args...)
		},
	})
	if err := listen(srv, instance.Ports(*port, remembered, foreign)); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if st != nil {
		err := saveInstance(st, instance.Info{Port: srv.Port(), PID: os.Getpid(), InstanceID: id, Token: token,
			StartedAt: srv.StartedAt(), Version: version})
		if err != nil {
			// This netdiag keeps the start lock, so no second one serves the
			// folder, but a later launch cannot find this one.
			fmt.Fprintf(stderr, "warning: could not save instance.json (%v). netdiag works, but opening it again will not find this window until you quit it.\n", err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Fprintf(stdout, "netdiag UI: %s (Ctrl+C to quit)\n", srv.URL())
	if srv.MCPEnabled() {
		fmt.Fprintf(stdout, "netdiag MCP: %s (enabled)\n", srv.MCPURL())
	} else {
		fmt.Fprintf(stdout, "netdiag MCP: %s (disabled — turn on in Settings > Agent access)\n", srv.MCPURL())
	}
	if *noBrowser {
		// The address alone opens a page whose buttons are off: the session
		// token only rides on the page netdiag opens (v0.6 4.1).
		fmt.Fprintln(stdout, openAgain)
	} else {
		if err := openBrowser(instance.PageURL(srv.Port(), token)); err != nil {
			fmt.Fprintln(stderr, "could not open browser:", err)
		}
	}
	if err := srv.Serve(ctx); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

// claim makes this launch the netdiag of st's data folder. When the netdiag
// in instance.json answers, it opens that page and returns code 0 and no
// lock; when another netdiag holds the start lock and never answers, code
// 1. Otherwise it returns the start lock (nil, with code -1, when the
// system has none to give: netdiag then runs without it), what
// instance.json held, and whether another netdiag answered on its port (or
// one that could not prove it is the netdiag in instance.json).
func claim(st *store.Store, noBrowser bool, stdout, stderr io.Writer) (instance.Info, *instance.Lock, bool, int) {
	ctx := context.Background()
	info, err := instance.Load(st)
	if err != nil {
		fmt.Fprintln(stderr, "warning: could not read instance.json:", err)
	}
	running, foreign := instance.Running(ctx, info)
	if running {
		return info, nil, false, openRunning(info, noBrowser, stdout, stderr)
	}
	lock, err := instance.Acquire(st.Dir())
	// Another netdiag holds the lock: it is starting, or about to stop.
	// Ask again until it answers or lets go.
	for deadline := time.Now().Add(lockWait); errors.Is(err, instance.ErrLocked) && time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
		info, _ = instance.Load(st)
		if running, foreign = instance.Running(ctx, info); running {
			return info, nil, false, openRunning(info, noBrowser, stdout, stderr)
		}
		lock, err = instance.Acquire(st.Dir())
	}
	if errors.Is(err, instance.ErrLocked) {
		fmt.Fprintf(stderr, "error: another netdiag is using the data folder %s but is not answering. Close it and try again.\n", st.Dir())
		return info, nil, false, 1
	}
	if err != nil {
		fmt.Fprintln(stderr, "warning: could not take the start lock:", err)
		lock = nil
	}
	return info, lock, foreign, -1
}

// openAgain is what a launch with --no-browser says: only a page netdiag
// opens has the key its buttons need.
const openAgain = "To open the page with its buttons working, run netdiag again without --no-browser."

// saveDelays are the waits between tries to write instance.json. A second
// launch reads the file every 100 ms while this one starts, and on Windows
// replacing a file fails while someone has it open (a virus scanner can
// too), so a failed write is tried again before netdiag gives up on it.
var saveDelays = []time.Duration{20 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond,
	200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond}

// saveInfo writes instance.json; tests replace it.
var saveInfo = instance.Save

// saveInstance writes instance.json, trying again after each of saveDelays.
func saveInstance(st *store.Store, info instance.Info) error {
	err := saveInfo(st, info)
	for _, d := range saveDelays {
		if err == nil {
			return nil
		}
		time.Sleep(d)
		err = saveInfo(st, info)
	}
	return err
}

// openRunning opens the page of the netdiag that is already running, with
// its session token, and reports it.
func openRunning(info instance.Info, noBrowser bool, stdout, stderr io.Writer) int {
	addr := fmt.Sprintf("http://127.0.0.1:%d", info.Port)
	if noBrowser {
		fmt.Fprintf(stdout, "netdiag is already running at %s.\n%s\n", addr, openAgain)
		return 0
	}
	if err := openBrowser(instance.PageURL(info.Port, info.Token)); err != nil {
		fmt.Fprintf(stdout, "netdiag is already running at %s.\n", addr)
		fmt.Fprintln(stderr, "could not open browser:", err)
		return 0
	}
	fmt.Fprintf(stdout, "netdiag is already running at %s. Opened it in your browser.\n", addr)
	return 0
}

// listen binds the first port of ports that is free (0 picks a random free
// port). A port given with --port is the only one tried.
func listen(srv *server.Server, ports []int) error {
	var err error
	for _, p := range ports {
		if err = srv.Listen(p); err == nil {
			return nil
		}
	}
	return err
}

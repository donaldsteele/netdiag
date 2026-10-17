package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"netdiag/internal/devtype"
	"netdiag/internal/model"
	"netdiag/internal/netinfo"
	"netdiag/internal/report"
	"netdiag/internal/scan"
)

// runScan implements `netdiag scan`. Exit codes: 0 ok, 1 runtime error
// (including cancel/timeout), 2 invalid arguments. With --live the scan
// keeps watching for new devices until Ctrl+C, which exits 0.
func runScan(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cidr := fs.String("cidr", "", "target CIDR or range (default: suggested subnet of the best interface)")
	community := fs.String("community", model.DefaultCommunity, "SNMP read community")
	asJSON := fs.Bool("json", false, "write the ScanResult as JSON to stdout")
	csvPath := fs.String("csv", "", "write CSV report to `FILE` (\"-\" for stdout)")
	timeout := fs.Duration("timeout", 60*time.Second, "overall scan timeout (with --live: only if set explicitly)")
	live := fs.Bool("live", false, "after the initial scan, keep watching for new devices until Ctrl+C")
	interval := fs.Duration("interval", 15*time.Second, "live mode check interval (5s..5m)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "error: unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintln(stderr, "error: --timeout must be positive")
		return 2
	}
	if *live && (*interval < 5*time.Second || *interval > 5*time.Minute) {
		fmt.Fprintln(stderr, "error: --interval must be between 5s and 5m")
		return 2
	}
	timeoutSet := false
	fs.Visit(func(f *flag.Flag) { timeoutSet = timeoutSet || f.Name == "timeout" })
	if *asJSON && *csvPath == "-" {
		fmt.Fprintln(stderr, "error: --json and --csv - both write to stdout")
		return 2
	}

	req := model.ScanRequest{CIDR: *cidr, Community: *community}
	if *live {
		req.Live = true
		req.LiveIntervalSec = int(*interval / time.Second)
	}
	if req.CIDR == "" {
		list, err := netinfo.Interfaces()
		if err != nil || len(list) == 0 {
			fmt.Fprintln(stderr, "error: no usable interface; pass --cidr")
			return 2
		}
		c, trunc := netinfo.SuggestTarget(list[0])
		req.CIDR, req.Interface = c, list[0].Name
		if trunc {
			fmt.Fprintf(stderr, "note: subnet truncated to %s\n", c)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if !*live || timeoutSet {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	s, err := scan.New(scan.DefaultDeps()).Start(ctx, req)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		if errors.Is(err, scan.ErrInvalidRequest) {
			return 2
		}
		return 1
	}
	fmt.Fprintf(stderr, "scanning %s ...\n", req.CIDR)
	events, unsub := s.Subscribe()
	lw := &liveWriter{stdout: stdout, stderr: stderr, json: *asJSON, latest: map[string]model.Host{}, printed: map[string]string{}}
	for ev := range events {
		switch ev.Type {
		case model.EventStage:
			fmt.Fprintf(stderr, "stage: %s\n", ev.Data.(model.StageInfo).Stage)
		case model.EventSnapshot:
			if err := lw.initial(ev.Data.(model.ScanResult), *csvPath); err != nil {
				fmt.Fprintln(stderr, "error:", err)
			}
		case model.EventHost:
			h := ev.Data.(model.Host)
			lw.latest[h.IP] = h
		case model.EventLive:
			lw.flushNew()
		case model.EventPresence:
			lw.presence(ev.Data.(model.Presence))
		case model.EventNotice:
			if n, ok := ev.Data.(model.NoticeEvent); ok {
				lw.notice(n)
			}
		case model.EventHealth:
			if h, ok := ev.Data.(model.HealthReport); ok {
				lw.health(h)
			}
		}
	}
	unsub()
	res := s.Wait()
	if lw.started {
		// Live mode: the initial result was already printed and Ctrl+C is
		// the normal way to stop, so exit 0 and only write a CSV file.
		lw.flushNew()
		fmt.Fprintf(stderr, "live stopped: %d scanned, %d alive, %d printers, %d web\n",
			res.Stats.Scanned, res.Stats.Alive, res.Stats.Printers, res.Stats.Web)
		if *csvPath != "" && *csvPath != "-" {
			if err := writeCSV(*csvPath, stdout, res); err != nil {
				fmt.Fprintln(stderr, "error:", err)
				return 1
			}
		}
		return 0
	}
	for _, w := range res.Warnings {
		fmt.Fprintln(stderr, "warning:", w)
	}
	fmt.Fprintf(stderr, "%s: %d scanned, %d alive, %d printers, %d web\n",
		res.State, res.Stats.Scanned, res.Stats.Alive, res.Stats.Printers, res.Stats.Web)

	code := 0
	if res.State != model.StateDone {
		code = 1
	}
	if *asJSON {
		if err := report.WriteJSON(stdout, res); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
	}
	if *csvPath != "" {
		if err := writeCSV(*csvPath, stdout, res); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
	}
	if !*asJSON && *csvPath != "-" {
		printTable(stdout, res)
	}
	return code
}

func writeCSV(path string, stdout io.Writer, res model.ScanResult) error {
	if path == "-" {
		return report.WriteCSV(stdout, res)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := report.WriteCSV(f, res); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func printTable(w io.Writer, res model.ScanResult) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "IP\tMAC\tVENDOR\tTYPE\tHOSTNAME\tFOUND BY\tPORTS\tPRINTER")
	for _, h := range report.SortHosts(res.Hosts) {
		ip := h.IP
		switch {
		case h.IsSelf:
			ip += " (self)"
		case h.IP == res.Gateway:
			ip += " (gw)"
		}
		ports := make([]string, len(h.OpenPorts))
		for i, p := range h.OpenPorts {
			ports[i] = fmt.Sprint(p)
		}
		pr := ""
		if h.IsPrinter {
			pr = "yes: " + h.PrinterReason
		}
		dt := devtype.Type(h.DeviceType)
		if h.DeviceType == "" {
			dt, _ = devtype.Classify(h, res.Gateway)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", ip, h.MAC, h.Vendor, devtype.Label(dt), h.Hostname,
			strings.Join(h.FoundBy, ","), strings.Join(ports, ","), pr)
	}
	tw.Flush()
}

// liveWriter prints the initial live-mode result, then each new host once
// after its probe has completed (signalled by the next live status event),
// and every online/offline transition as it happens.
type liveWriter struct {
	stdout, stderr io.Writer
	json           bool
	started        bool
	healthSum      string
	latest         map[string]model.Host
	printed        map[string]string // ip -> firstSeen of the printed record
}

// presence prints one online/offline transition.
func (l *liveWriter) presence(p model.Presence) {
	if !l.started {
		return
	}
	if l.json {
		if b, err := json.Marshal(model.Event{Type: model.EventPresence, Data: p}); err == nil {
			fmt.Fprintf(l.stdout, "%s\n", b)
		}
		return
	}
	state := "OFFLINE"
	if p.Online {
		state = "ONLINE "
	}
	line := time.Now().Format("15:04:05") + "  " + state + "  " + p.IP
	if h, ok := l.latest[p.IP]; ok && h.Vendor != "" {
		line += "  " + h.Vendor
	}
	if p.Reason != "" {
		line += "  (" + p.Reason + ")"
	}
	fmt.Fprintln(l.stdout, line)
}

// notice prints one alert line (NDJSON with --json). Replayed notices and
// anything before the initial result are skipped.
func (l *liveWriter) notice(n model.NoticeEvent) {
	if !l.started || n.Replay {
		return
	}
	if l.json {
		if b, err := json.Marshal(model.Event{Type: model.EventNotice, Data: n}); err == nil {
			fmt.Fprintf(l.stdout, "%s\n", b)
		}
		return
	}
	if n.OK {
		line := time.Now().Format("15:04:05") + "  FIXED  " + n.Title
		if n.IP != "" {
			line += "  " + n.IP
		}
		if d, ok := downFor(n.Notice); ok {
			line += "  (was down " + d + ")"
		}
		fmt.Fprintln(l.stdout, line)
		return
	}
	line := time.Now().Format("15:04:05") + "  ALERT  [" + n.Severity + "]  " + n.Title
	if n.IP != "" {
		line += "  " + n.IP
	}
	if n.Message != "" {
		line += "  " + n.Message
	}
	fmt.Fprintln(l.stdout, line)
}

// downFor is how long an OK notice's problem was open, such as "12m" or
// "1h5m" (whole minutes; "<1m" under a minute). It reports false when the
// times are missing or unreadable.
func downFor(n model.Notice) (string, bool) {
	from, err1 := time.Parse(time.RFC3339, n.OpenedAt)
	to, err2 := time.Parse(time.RFC3339, n.At)
	if err1 != nil || err2 != nil || to.Before(from) {
		return "", false
	}
	d := to.Sub(from).Truncate(time.Minute)
	switch {
	case d < time.Minute:
		return "<1m", true
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes())), true
	}
	return strings.TrimSuffix(d.String(), "0s"), true
}

// health prints a one-line health summary on stderr, once, when the first
// report arrives (and again only if the worst status changes).
func (l *liveWriter) health(h model.HealthReport) {
	counts := map[string]int{}
	for _, c := range h.Checks {
		counts[c.Status]++
	}
	sum := fmt.Sprintf("%d fail, %d warn, %d ok", counts[model.HealthFail], counts[model.HealthWarn], counts[model.HealthOK])
	if sum == l.healthSum {
		return
	}
	l.healthSum = sum
	fmt.Fprintln(l.stderr, "health: "+sum)
	for _, c := range h.Checks {
		if c.Status == model.HealthFail || c.Status == model.HealthWarn {
			fmt.Fprintf(l.stderr, "  %s: %s - %s\n", c.Status, c.Label, c.Detail)
		}
	}
}

func (l *liveWriter) initial(res model.ScanResult, csvPath string) error {
	l.started = true
	for _, w := range res.Warnings {
		fmt.Fprintln(l.stderr, "warning:", w)
	}
	fmt.Fprintf(l.stderr, "initial scan: %d scanned, %d alive, %d printers, %d web; watching for new devices (Ctrl+C to stop)\n",
		res.Stats.Scanned, res.Stats.Alive, res.Stats.Printers, res.Stats.Web)
	if l.json {
		return report.WriteJSON(l.stdout, res)
	}
	if csvPath == "-" {
		return report.WriteCSV(l.stdout, res)
	}
	printTable(l.stdout, res)
	return nil
}

func (l *liveWriter) flushNew() {
	if !l.started {
		return
	}
	var fresh []model.Host
	for ip, h := range l.latest {
		// A changed firstSeen means the IP now belongs to a different device.
		if seen, ok := l.printed[ip]; h.IsNew && (!ok || seen != h.FirstSeen) {
			fresh = append(fresh, h)
		}
	}
	for _, h := range report.SortHosts(fresh) {
		l.printed[h.IP] = h.FirstSeen
		if l.json {
			if b, err := json.Marshal(model.Event{Type: model.EventHost, Data: h}); err == nil {
				fmt.Fprintf(l.stdout, "%s\n", b)
			}
			continue
		}
		line := time.Now().Format("15:04:05") + "  NEW  " + h.IP
		for _, v := range []string{h.MAC, h.Vendor, h.Hostname} {
			if v != "" {
				line += "  " + v
			}
		}
		if len(h.OpenPorts) > 0 {
			line += "  ports " + strings.Trim(fmt.Sprint(h.OpenPorts), "[]")
		}
		if h.IsPrinter {
			line += "  printer: " + h.PrinterReason
		}
		fmt.Fprintln(l.stdout, line)
	}
}

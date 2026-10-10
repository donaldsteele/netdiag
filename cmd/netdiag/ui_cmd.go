package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/pkg/browser"

	"netdiag/internal/scan"
	"netdiag/internal/server"
	"netdiag/web"
)

// runUI implements `netdiag ui` (and the no-argument default).
func runUI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	port := fs.Int("port", 0, "port to listen on (default: random free port)")
	noBrowser := fs.Bool("no-browser", false, "do not open the default browser")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 || *port < 0 || *port > 65535 || *port == 80 || *port == 443 {
		fmt.Fprintln(stderr, "error: invalid arguments")
		return 2
	}
	srv := server.New(server.Config{
		Registry: scan.NewRegistry(scan.New(scan.DefaultDeps())),
		Static:   web.FS(),
	})
	if err := srv.Listen(*port); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Fprintf(stdout, "netdiag UI: %s (Ctrl+C to quit)\n", srv.URL())
	if !*noBrowser {
		if err := browser.OpenURL(srv.URL()); err != nil {
			fmt.Fprintln(stderr, "could not open browser:", err)
		}
	}
	if err := srv.Serve(ctx); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

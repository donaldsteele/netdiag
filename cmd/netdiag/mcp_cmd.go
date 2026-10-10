package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"netdiag/internal/mcpserver"
	"netdiag/internal/scan"
	"netdiag/internal/service"
	"netdiag/internal/store"
)

// nopWriteCloser keeps the caller's writer open when the transport closes.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// runMCP implements `netdiag mcp`: an MCP server over stdio. stdout carries
// only protocol frames; all diagnostics go to stderr.
func runMCP(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	storeDir := fs.String("store-dir", "", "data directory for settings and history (default: user config dir)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "error: invalid arguments")
		return 2
	}
	log.SetOutput(stderr)
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	deps := scan.DefaultDeps()
	st, err := store.Open(*storeDir)
	if err != nil {
		fmt.Fprintln(stderr, "warning: settings/history unavailable:", err)
		st = nil
		if *storeDir != "" {
			deps.Store, deps.Settings = nil, nil
		}
	} else if *storeDir != "" {
		deps.Store, deps.Settings = st, st
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()

	reg := scan.NewRegistry(scan.New(deps))
	svc := service.New(service.Config{Registry: reg, Store: st, BaseCtx: baseCtx})
	srv := mcpserver.New(svc, mcpserver.Options{Transport: "stdio", Version: version, Logger: logger})

	rc, ok := stdin.(io.ReadCloser)
	if !ok {
		rc = io.NopCloser(stdin)
	}
	code := 0
	if err := srv.Run(ctx, &mcp.IOTransport{Reader: rc, Writer: nopWriteCloser{stdout}}); err != nil && ctx.Err() == nil {
		fmt.Fprintln(stderr, "error:", err)
		code = 1
	}

	// Stop running scans and give the history checkpoint a moment to finish.
	cancelBase()
	if s, ok := reg.Running(); ok {
		s.Cancel()
		select {
		case <-s.Done():
		case <-time.After(3 * time.Second):
		}
	}
	return code
}

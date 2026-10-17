package mcpserver

import (
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"netdiag/internal/service"
)

// Options configures New.
type Options struct {
	// Transport is "stdio" or "http". Live mode is refused over stdio.
	Transport string
	Version   string
	// Logger receives SDK diagnostics (must not write to stdout). Optional.
	Logger *slog.Logger
}

const instructions = "netdiag discovers devices on a local network (ICMP, ARP, ports, SNMP, mDNS, IPP), runs network health checks, detects IP/MAC/name conflicts and keeps scan history. " +
	"Typical flow: call start_scan (it waits up to waitSec and returns a compact summary; if it is still running, poll get_scan), then list_devices with filter=problems, then get_device for details on one host; " +
	"use get_events with the returned nextSeq to follow live changes, and compare_scans to see what changed since a previous scan. " +
	"List results are paginated: pass nextCursor back as cursor. Scans and probes send real network traffic like the Scan button in the UI. " +
	"Agents cannot change settings, delete history, ignore conflicting MACs or quit netdiag: direct the operator to the netdiag UI for those actions. " +
	"SNMP community strings are always shown as \"***\"."

// New builds the MCP server exposing svc to agents.
func New(svc *service.Service, opts Options) *mcp.Server {
	if opts.Version == "" {
		opts.Version = "dev"
	}
	s := mcp.NewServer(
		&mcp.Implementation{Name: "netdiag", Title: "netdiag network diagnostics", Version: opts.Version},
		&mcp.ServerOptions{Instructions: instructions, Logger: opts.Logger},
	)
	registerTools(s, svc, opts)
	registerResources(s, svc)
	registerPrompts(s, svc)
	return s
}

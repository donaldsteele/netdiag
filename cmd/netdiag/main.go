// Command netdiag is a local network diagnostic tool.
package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"netdiag/internal/netinfo"
)

var version = "dev"

func usage() {
	fmt.Fprintln(os.Stderr, "usage: netdiag [ui [flags] | scan [flags] | history | compare [flags] | interfaces | mcp [flags] | version]")
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		return runUI(nil, os.Stdout, os.Stderr)
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "version":
		if fs.Parse(args[1:]) != nil {
			return 2
		}
		fmt.Printf("netdiag %s\n", version)
	case "ui":
		return runUI(args[1:], os.Stdout, os.Stderr)
	case "scan":
		return runScan(args[1:], os.Stdout, os.Stderr)
	case "history":
		return runHistory(args[1:], os.Stdout, os.Stderr)
	case "compare":
		return runCompare(args[1:], os.Stdout, os.Stderr)
	case "mcp":
		return runMCP(args[1:], os.Stdin, os.Stdout, os.Stderr)
	case "interfaces":
		if fs.Parse(args[1:]) != nil {
			return 2
		}
		return runInterfaces()
	default:
		usage()
		return 2
	}
	return 0
}

func runInterfaces() int {
	list, err := netinfo.Interfaces()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if len(list) == 0 {
		fmt.Println("no usable IPv4 interfaces found")
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tIP\tCIDR\tMAC\tDEFAULT")
	for _, f := range list {
		d := ""
		if f.IsDefault {
			d = "*"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", f.Name, f.IP, f.CIDR, f.MAC, d)
	}
	tw.Flush()
	cidr, trunc := netinfo.SuggestTarget(list[0])
	note := ""
	if trunc {
		note = " (narrowed to /24)"
	}
	fmt.Printf("\nSuggested target: %s%s\n", cidr, note)
	return 0
}

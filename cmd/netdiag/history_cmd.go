package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"netdiag/internal/diff"
	"netdiag/internal/model"
	"netdiag/internal/store"
)

// runHistory implements `netdiag history`: a table of saved scans.
func runHistory(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "error: unexpected arguments")
		return 2
	}
	st, err := store.Open("")
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	list, err := st.ListHistory()
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "no saved scans")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTARTED\tCIDR\tSTATE\tLIVE\tALIVE\tPRINTERS\tCONFLICTS")
	for _, e := range list {
		if e.Error != "" {
			fmt.Fprintf(tw, "%s\t(%s)\t\t\t\t\t\t\n", e.ID, e.Error)
			continue
		}
		live := ""
		if e.Live {
			live = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\n", e.ID, e.StartedAt, e.CIDR, e.State, live,
			e.Stats.Alive, e.Stats.Printers, e.Stats.Conflicts)
	}
	tw.Flush()
	return 0
}

// runCompare implements `netdiag compare [--base ID] [--current ID] [--json|--csv FILE]`.
// Defaults: current is the newest saved scan, base the previous scan of the
// same CIDR.
func runCompare(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	baseID := fs.String("base", "", "baseline scan id (default: previous scan of the same CIDR)")
	curID := fs.String("current", "", "current scan id (default: newest saved scan)")
	asJSON := fs.Bool("json", false, "write the diff as JSON to stdout")
	csvPath := fs.String("csv", "", "write the diff as CSV to `FILE` (\"-\" for stdout)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "error: unexpected arguments")
		return 2
	}
	if *asJSON && *csvPath == "-" {
		fmt.Fprintln(stderr, "error: --json and --csv - both write to stdout")
		return 2
	}
	st, err := store.Open("")
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if *curID == "" {
		list, err := st.ListHistory()
		if err == nil {
			for _, e := range list {
				if e.Error == "" {
					*curID = e.ID
					break
				}
			}
		}
		if *curID == "" {
			fmt.Fprintln(stderr, "error: no saved scans to compare")
			return 1
		}
	}
	cur, err := st.LoadScan(*curID)
	if err != nil {
		return compareFail(stderr, "current", *curID, err)
	}
	if *baseID == "" {
		e, ok, err := st.Previous(cur.Request.CIDR, cur.ID)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		if !ok {
			fmt.Fprintf(stderr, "error: no previous scan of %s to compare with\n", cur.Request.CIDR)
			return 1
		}
		*baseID = e.ID
	}
	if *baseID == cur.ID {
		fmt.Fprintln(stderr, "error: base and current must differ")
		return 2
	}
	base, err := st.LoadScan(*baseID)
	if err != nil {
		return compareFail(stderr, "base", *baseID, err)
	}
	d := diff.Compare(base, cur)
	switch {
	case *asJSON:
		err = diff.WriteJSON(stdout, d)
	case *csvPath == "-":
		err = diff.WriteCSV(stdout, d)
	case *csvPath != "":
		err = writeDiffCSV(*csvPath, d)
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if !*asJSON && *csvPath != "-" {
		printDiff(stdout, d)
	}
	return 0
}

func compareFail(stderr io.Writer, which, id string, err error) int {
	if errors.Is(err, store.ErrNotFound) {
		fmt.Fprintf(stderr, "error: %s scan %q not found (see `netdiag history`)\n", which, id)
	} else {
		fmt.Fprintln(stderr, "error:", err)
	}
	return 1
}

func writeDiffCSV(path string, d model.Diff) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := diff.WriteCSV(f, d); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func printDiff(w io.Writer, d model.Diff) {
	fmt.Fprintf(w, "base:    %s (%s, %s)\ncurrent: %s (%s, %s)\n", d.Base.ID, d.Base.CIDR, d.Base.StartedAt,
		d.Current.ID, d.Current.CIDR, d.Current.StartedAt)
	fmt.Fprintf(w, "added %d, removed %d, changed %d\n", d.Summary.Added, d.Summary.Removed, d.Summary.Changed)
	for _, h := range d.Added {
		fmt.Fprintf(w, "+ %s  %s  %s\n", h.IP, h.MAC, h.Vendor)
	}
	for _, h := range d.Removed {
		fmt.Fprintf(w, "- %s  %s  %s\n", h.IP, h.MAC, h.Vendor)
	}
	for _, c := range d.Changed {
		fmt.Fprintf(w, "~ %s  %s\n", c.IP, c.MAC)
		for _, f := range c.Changes {
			fmt.Fprintf(w, "    %s: %s -> %s\n", f.Field, f.From, f.To)
		}
	}
}

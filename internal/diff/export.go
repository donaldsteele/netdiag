package diff

import (
	"encoding/csv"
	"encoding/json"
	"io"

	"netdiag/internal/model"
)

// CSVHeader is the column list of the diff CSV export.
var CSVHeader = []string{"change", "ip", "mac", "vendor", "field", "from", "to"}

// WriteJSON writes the diff as indented JSON.
func WriteJSON(w io.Writer, d model.Diff) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(d)
}

// WriteCSV writes the diff as CSV: one row per added/removed host and one per
// changed field.
func WriteCSV(w io.Writer, d model.Diff) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(CSVHeader); err != nil {
		return err
	}
	for _, h := range d.Added {
		if err := cw.Write([]string{"added", h.IP, h.MAC, h.Vendor, "", "", ""}); err != nil {
			return err
		}
	}
	for _, h := range d.Removed {
		if err := cw.Write([]string{"removed", h.IP, h.MAC, h.Vendor, "", "", ""}); err != nil {
			return err
		}
	}
	for _, c := range d.Changed {
		for _, f := range c.Changes {
			if err := cw.Write([]string{"changed", c.IP, c.MAC, c.Vendor, f.Field, f.From, f.To}); err != nil {
				return err
			}
		}
	}
	cw.Flush()
	return cw.Error()
}

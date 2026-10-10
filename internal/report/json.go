package report

import (
	"encoding/json"
	"io"

	"netdiag/internal/model"
)

// WriteJSON writes res as indented JSON followed by a newline. Hosts are
// emitted in numeric IP order; the caller's ScanResult is not modified.
func WriteJSON(w io.Writer, res model.ScanResult) error {
	res.Hosts = SortHosts(res.Hosts)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

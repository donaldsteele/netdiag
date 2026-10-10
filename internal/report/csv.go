package report

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"

	"netdiag/internal/model"
)

// CSVColumns is the fixed column order of the CSV export (plan Section 4).
var CSVColumns = []string{
	"ip", "mac", "vendor", "hostname", "alive", "found_by", "rtt_ms",
	"open_ports", "is_printer", "printer_reason", "model", "serial",
	"page_count", "supplies", "printer_status", "errors", "web_urls", "web_titles",
	"online", "last_seen",
	"mdns_name", "mdns_services", "printer_source", "conflicts",
}

// WriteCSV writes one row per host (numeric IP order) after a header row.
// Records are RFC 4180 encoded with CRLF line endings. Multi-valued fields are
// joined with "; ".
func WriteCSV(w io.Writer, res model.ScanResult) error {
	cw := csv.NewWriter(w)
	cw.UseCRLF = true
	if err := cw.Write(CSVColumns); err != nil {
		return err
	}
	for _, h := range SortHosts(res.Hosts) {
		if err := cw.Write(csvRow(h)); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func csvRow(h model.Host) []string {
	var (
		printerModel, serial, pages, supplies, printerStatus, printerSource string
	)
	if h.Printer != nil {
		printerModel = h.Printer.Model
		serial = h.Printer.Serial
		pages = strconv.FormatInt(h.Printer.PageCount, 10)
		supplies = formatSupplies(h.Printer.Supplies)
		printerSource = h.Printer.Source
	}
	if h.Snmp != nil {
		printerStatus = h.Snmp.HrPrinterStatus
	}

	var mdnsName string
	var mdnsServices []string
	if h.MDNS != nil {
		mdnsName = h.MDNS.Instance
		if mdnsName == "" {
			mdnsName = h.MDNS.Hostname
		}
		mdnsServices = h.MDNS.Services
	}

	urls := make([]string, 0, len(h.Web))
	titles := make([]string, 0, len(h.Web))
	for _, ws := range h.Web {
		urls = append(urls, ws.URL)
		titles = append(titles, ws.Title)
	}

	return []string{
		h.IP,
		h.MAC,
		h.Vendor,
		h.Hostname,
		strconv.FormatBool(h.Alive),
		join(h.FoundBy),
		formatRtt(h.RttMs),
		formatPorts(h.OpenPorts),
		strconv.FormatBool(h.IsPrinter),
		h.PrinterReason,
		printerModel,
		serial,
		pages,
		supplies,
		printerStatus,
		join(h.Errors),
		join(urls),
		join(titles),
		strconv.FormatBool(h.Online),
		h.LastSeen,
		mdnsName,
		join(mdnsServices),
		printerSource,
		join(h.Conflicts),
	}
}

// formatRtt renders the round-trip time in milliseconds to one decimal place,
// or an empty string when no reply time was recorded.
func formatRtt(ms float64) string {
	if ms <= 0 {
		return ""
	}
	return strconv.FormatFloat(ms, 'f', 1, 64)
}

func formatPorts(ports []int) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, strconv.Itoa(p))
	}
	return join(parts)
}

// formatSupplies renders supplies as "Description=NN%". Supplies without a
// known percentage render as "Description=unknown".
func formatSupplies(supplies []model.Supply) string {
	parts := make([]string, 0, len(supplies))
	for _, s := range supplies {
		if s.Percent < 0 {
			parts = append(parts, fmt.Sprintf("%s=unknown", s.Description))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%d%%", s.Description, s.Percent))
	}
	return join(parts)
}

func join(parts []string) string {
	return strings.Join(parts, "; ")
}

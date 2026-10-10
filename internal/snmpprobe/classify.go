package snmpprobe

import (
	"fmt"

	"netdiag/internal/model"
)

// Classify decides whether h is a printer. Rules in order: SNMP hrDevicePrinter /
// hrPrinter table, Printer-MIB data, then ports 9100/631/515.
func Classify(h *model.Host) (bool, string) {
	if h.Snmp != nil && h.Snmp.HrPrinterStatus != "" {
		return true, "snmp: hrDeviceType is printer"
	}
	if h.Printer != nil {
		return true, "snmp: Printer-MIB (prtGeneral) present"
	}
	for _, w := range []struct {
		port int
		name string
	}{{9100, "JetDirect/RAW"}, {631, "IPP"}, {515, "LPD"}} {
		for _, p := range h.OpenPorts {
			if p == w.port {
				return true, fmt.Sprintf("port %d open (%s)", w.port, w.name)
			}
		}
	}
	return false, ""
}

// Apply runs Classify and stores the result on h.
func Apply(h *model.Host) {
	h.IsPrinter, h.PrinterReason = Classify(h)
}

package snmpprobe

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"netdiag/internal/model"
)

// StatusDeadline bounds one PollStatus call.
const StatusDeadline = 2 * time.Second

// StatusTarget is what a full Probe learned about where a printer keeps its
// status: the SNMP version that answered and the hrDeviceTable /
// hrPrinterTable row indexes.
type StatusTarget struct {
	Version string
	DevIdx  string // hrDeviceTable index; "" when the device row was not found
	PrtIdx  string // hrPrinterTable index
}

// Status is the cheap, frequently polled part of a printer's state. Values
// use the same names as model.SnmpInfo.
type Status struct {
	DeviceStatus  string
	PrinterStatus string
	ErrorState    []string
}

// Key is a stable fingerprint of the alert-relevant part (device status and
// error bits) for change detection. Printer status is left out so normal
// idle/printing flips do not trigger full re-reads.
func (s Status) Key() string {
	e := slices.Clone(s.ErrorState)
	slices.Sort(e)
	return s.DeviceStatus + "|" + strings.Join(e, ",")
}

// StatusOf extracts the polled fields from a full probe result.
func StatusOf(si *model.SnmpInfo) Status {
	if si == nil {
		return Status{}
	}
	return Status{DeviceStatus: si.HrDeviceStatus, PrinterStatus: si.HrPrinterStatus, ErrorState: si.HrPrinterDetectedErrorState}
}

// PollStatus reads only hrDeviceStatus, hrPrinterStatus and
// hrPrinterDetectedErrorState for a known printer in a single GET. A missing
// object (e.g. the agent renumbered its tables after a reboot) is an error so
// the caller falls back to a full Probe.
func PollStatus(ctx context.Context, dial Dialer, ip, community string, t StatusTarget) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, StatusDeadline)
	defer cancel()
	c, err := dial(ctx, ip, community, t.Version)
	if err != nil {
		return Status{}, err
	}
	defer c.Close()
	stOID := fmt.Sprintf("%s.%d.%s", OIDHrPrinterTable, colHrPrtStatus, t.PrtIdx)
	esOID := fmt.Sprintf("%s.%d.%s", OIDHrPrinterTable, colHrPrtErrState, t.PrtIdx)
	oids := []string{stOID, esOID}
	devOID := ""
	if t.DevIdx != "" {
		devOID = fmt.Sprintf("%s.%d.%s", OIDHrDeviceTable, colHrDevStatus, t.DevIdx)
		oids = append(oids, devOID)
	}
	pdus, err := c.Get(ctx, oids)
	if err != nil {
		return Status{}, err
	}
	got := map[string]PDU{}
	for _, p := range pdus {
		got[strings.TrimPrefix(p.OID, ".")] = p
	}
	var s Status
	p, ok := got[stOID]
	n, isNum := num(p)
	if !ok || !isNum {
		return Status{}, fmt.Errorf("snmp: %s missing", stOID)
	}
	s.PrinterStatus = printerStatus(n)
	if e, ok := got[esOID]; ok && e.Kind != KindNull {
		s.ErrorState = errorStates(e.Bytes)
	}
	if devOID != "" {
		if n, ok := num(got[devOID]); ok {
			s.DeviceStatus = deviceStatus(n)
		}
	}
	return s, nil
}

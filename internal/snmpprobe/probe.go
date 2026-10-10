package snmpprobe

import (
	"context"
	"fmt"
	"strings"
	"time"

	"netdiag/internal/model"
)

// HostDeadline is the total SNMP budget per host.
const HostDeadline = 5 * time.Second

// Result is the outcome of probing one host.
type Result struct {
	Snmp    *model.SnmpInfo
	Printer *model.PrinterInfo // non-nil only when printer evidence was found
	Errors  []string           // partial-failure notes, "snmp: ..." prefixed
	// Status locates the printer status objects for PollStatus (nil when the
	// host has no hrPrinterTable row).
	Status *StatusTarget
}

// Probe queries ip. A sysDescr GET gates all walks: v2c first, v1 fallback only
// if v2c fails. Returns an error wrapping ErrNoResponse if the gate fails on both.
func Probe(ctx context.Context, dial Dialer, ip, community string) (*Result, error) {
	if community == "" {
		community = model.DefaultCommunity
	}
	ctx, cancel := context.WithTimeout(ctx, HostDeadline)
	defer cancel()

	var cl Client
	var sys []PDU
	var version string
	var lastErr error
	for _, v := range []string{"v2c", "v1"} {
		c, err := dial(ctx, ip, community, v)
		if err != nil {
			lastErr = err
			continue
		}
		pdus, err := c.Get(ctx, []string{OIDSysDescr, OIDSysObjectID, OIDSysUpTime, OIDSysContact, OIDSysName, OIDSysLocation})
		if err == nil && gateOK(pdus) {
			cl, sys, version = c, pdus, v
			break
		}
		if err == nil {
			err = fmt.Errorf("empty sysDescr")
		}
		lastErr = err
		_ = c.Close()
		if ctx.Err() != nil {
			break
		}
	}
	if cl == nil {
		return nil, fmt.Errorf("%w: %v", ErrNoResponse, lastErr)
	}
	defer cl.Close()

	r := &Result{Snmp: &model.SnmpInfo{Version: version}}
	sv := map[string]PDU{}
	for _, p := range sys {
		sv[p.OID] = p
	}
	si := r.Snmp
	si.SysDescr = text(sv[OIDSysDescr])
	si.SysObjectID = text(sv[OIDSysObjectID])
	si.SysName = text(sv[OIDSysName])
	si.SysLocation = text(sv[OIDSysLocation])
	si.SysContact = text(sv[OIDSysContact])
	si.SysUpTime = uptime(sv[OIDSysUpTime])

	walk := func(root string) []PDU {
		if ctx.Err() != nil {
			return nil
		}
		p, err := cl.Walk(ctx, root)
		if err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("snmp: walk %s: %v", root, err))
		}
		return p
	}

	devs := walk(OIDHrDeviceTable)
	prts := walk(OIDHrPrinterTable)
	gen := walk(OIDPrtGeneral)

	// Locate the printer device in hrDeviceTable.
	types := column(devs, OIDHrDeviceTable, colHrDevType)
	descrs := column(devs, OIDHrDeviceTable, colHrDevDescr)
	stats := column(devs, OIDHrDeviceTable, colHrDevStatus)
	prtIdx := ""
	for _, k := range keysOf(types) {
		if types[k].Kind == KindOID && types[k].Str == OIDHrDevicePrinter {
			prtIdx = k
			break
		}
	}
	isPrinter := prtIdx != ""
	if isPrinter {
		si.HrDeviceDescr = text(descrs[prtIdx])
		if n, ok := num(stats[prtIdx]); ok {
			si.HrDeviceStatus = deviceStatus(n)
		}
	}
	pst := column(prts, OIDHrPrinterTable, colHrPrtStatus)
	pes := column(prts, OIDHrPrinterTable, colHrPrtErrState)
	pk := prtIdx
	if _, ok := pst[pk]; !ok {
		if ks := keysOf(pst); len(ks) > 0 {
			pk = ks[0]
		}
	}
	if p, ok := pst[pk]; ok {
		r.Status = &StatusTarget{Version: version, DevIdx: prtIdx, PrtIdx: pk}
		if n, ok := num(p); ok {
			si.HrPrinterStatus = printerStatus(n)
		} else {
			si.HrPrinterStatus = "unknown"
		}
		if e, ok := pes[pk]; ok {
			si.HrPrinterDetectedErrorState = errorStates(e.Bytes)
		}
		isPrinter = true
	} else if isPrinter {
		si.HrPrinterStatus = "unknown"
	}

	serials := column(gen, OIDPrtGeneral, colGenSerial)
	serial := ""
	for _, k := range keysOf(serials) {
		if s := text(serials[k]); s != "" {
			serial = s
			break
		}
	}
	if !isPrinter && len(gen) == 0 {
		return r, nil
	}

	pi := &model.PrinterInfo{Serial: serial, Model: si.HrDeviceDescr}
	if pi.Model == "" {
		pi.Model = si.SysDescr
	}
	r.Printer = pi
	// Page count: highest life count across markers.
	for _, p := range walk(OIDPrtLifeCount) {
		if n, ok := num(p); ok && n > pi.PageCount {
			pi.PageCount = n
		}
	}

	// Supplies with colorant join.
	sup := walk(OIDPrtSupplies)
	colorants := map[string]PDU{}
	for _, p := range walk(OIDPrtColorant) {
		colorants[strings.TrimPrefix(p.OID, OIDPrtColorant+".")] = p
	}
	sTypes := column(sup, OIDPrtSupplies, colSupType)
	sDescr := column(sup, OIDPrtSupplies, colSupDescr)
	sUnit := column(sup, OIDPrtSupplies, colSupUnit)
	sMax := column(sup, OIDPrtSupplies, colSupMax)
	sLvl := column(sup, OIDPrtSupplies, colSupLevel)
	sCol := column(sup, OIDPrtSupplies, colSupColorantIdx)
	for _, k := range keysOf(sTypes, sDescr, sMax, sLvl) {
		s := model.Supply{Description: text(sDescr[k]), Unit: "unknown"}
		if n, ok := num(sTypes[k]); ok {
			if s.Type = supplyTypes[n]; s.Type == "" {
				s.Type = "other"
			}
		}
		if n, ok := num(sUnit[k]); ok {
			if u := supplyUnits[n]; u != "" {
				s.Unit = u
			}
		}
		lvl, lok := num(sLvl[k])
		mx, mok := num(sMax[k])
		if !lok {
			lvl = -2
		}
		if !mok {
			mx = -2
		}
		// Unit "percent" with unknown max: level is already a percentage.
		if s.Unit == "percent" && mx <= 0 && lvl >= 0 {
			mx = 100
		}
		s.Level, s.Max = int(lvl), int(mx)
		s.Percent, s.State = supplyPercent(lvl, mx)
		// Colorant join: supply row hostIdx.supplyIdx -> colorant row hostIdx.colorantIdx.
		if ci, ok := num(sCol[k]); ok && ci > 0 {
			host := k
			if i := strings.LastIndex(k, "."); i >= 0 {
				host = k[:i]
			}
			if c, ok := colorants[fmt.Sprintf("%s.%d", host, ci)]; ok {
				s.Color = strings.ToLower(text(c))
			}
		}
		if s.Color == "" {
			s.Color = colorFromText(s.Description)
		}
		pi.Supplies = append(pi.Supplies, s)
	}

	// Input trays.
	in := walk(OIDPrtInput)
	iMax := column(in, OIDPrtInput, colInMax)
	iCur := column(in, OIDPrtInput, colInCur)
	iSt := column(in, OIDPrtInput, colInStatus)
	iMed := column(in, OIDPrtInput, colInMedia)
	iName := column(in, OIDPrtInput, colInName)
	for _, k := range keysOf(iMax, iCur, iSt, iMed, iName) {
		t := model.InputTray{Name: text(iName[k]), MediaName: text(iMed[k]), Level: -2, Max: -2}
		if n, ok := num(iCur[k]); ok {
			t.Level = int(n)
		}
		if n, ok := num(iMax[k]); ok {
			t.Max = int(n)
		}
		if n, ok := num(iSt[k]); ok {
			t.Status = inputStatus(n)
		}
		pi.InputTrays = append(pi.InputTrays, t)
	}

	// Covers.
	cv := walk(OIDPrtCover)
	cD := column(cv, OIDPrtCover, colCoverDescr)
	cS := column(cv, OIDPrtCover, colCoverStatus)
	for _, k := range keysOf(cD, cS) {
		c := model.Cover{Description: text(cD[k]), Status: "unknown"}
		if n, ok := num(cS[k]); ok {
			c.Status = coverStatus(n)
		}
		pi.Covers = append(pi.Covers, c)
	}

	// Alerts.
	al := walk(OIDPrtAlert)
	aS := column(al, OIDPrtAlert, colAlertSeverity)
	aC := column(al, OIDPrtAlert, colAlertCode)
	aM := column(al, OIDPrtAlert, colAlertMsg)
	for _, k := range keysOf(aS, aC, aM) {
		a := model.Alert{Severity: "other", Code: text(aC[k]), Description: text(aM[k])}
		if n, ok := num(aS[k]); ok {
			a.Severity = severity(n)
		}
		pi.Alerts = append(pi.Alerts, a)
	}
	if len(pi.Alerts) == 0 {
		pi.Alerts = statusAlerts(si)
	}

	// Console display lines (rows are OIDPrtConsoleBuffer.<host>.<line>).
	cb := map[string]PDU{}
	for _, p := range walk(OIDPrtConsoleBuffer) {
		if strings.HasPrefix(p.OID, OIDPrtConsoleBuffer+".") {
			cb[strings.TrimPrefix(p.OID, OIDPrtConsoleBuffer+".")] = p
		}
	}
	for _, k := range keysOf(cb) {
		if s := text(cb[k]); s != "" {
			pi.ConsoleDisplay = append(pi.ConsoleDisplay, s)
		}
	}
	return r, nil
}

func gateOK(pdus []PDU) bool {
	for _, p := range pdus {
		if p.OID == OIDSysDescr {
			return p.Kind != KindNull
		}
	}
	return false
}

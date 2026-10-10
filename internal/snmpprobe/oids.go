// Package snmpprobe interrogates hosts over SNMP (read-only v2c with v1 fallback)
// and classifies printers.
package snmpprobe

// System group scalars.
const (
	OIDSysDescr    = "1.3.6.1.2.1.1.1.0"
	OIDSysObjectID = "1.3.6.1.2.1.1.2.0"
	OIDSysUpTime   = "1.3.6.1.2.1.1.3.0"
	OIDSysContact  = "1.3.6.1.2.1.1.4.0"
	OIDSysName     = "1.3.6.1.2.1.1.5.0"
	OIDSysLocation = "1.3.6.1.2.1.1.6.0"
)

// Table roots walked with GETBULK.
const (
	OIDHrDeviceTable    = "1.3.6.1.2.1.25.3.2.1" // type .2, descr .3, status .5
	OIDHrPrinterTable   = "1.3.6.1.2.1.25.3.5.1" // status .1, detectedErrorState .2
	OIDHrDevicePrinter  = "1.3.6.1.2.1.25.3.1.5" // hrDeviceType value for printers
	OIDPrtGeneral       = "1.3.6.1.2.1.43.5.1.1" // serial .17
	OIDPrtLifeCount     = "1.3.6.1.2.1.43.10.2.1.4"
	OIDPrtSupplies      = "1.3.6.1.2.1.43.11.1.1" // colorantIdx .4, type .5, descr .6, unit .7, max .8, level .9
	OIDPrtColorant      = "1.3.6.1.2.1.43.12.1.1.4"
	OIDPrtInput         = "1.3.6.1.2.1.43.8.2.1"  // max .9, cur .10, status .11, media .12, name .13
	OIDPrtCover         = "1.3.6.1.2.1.43.6.1.1"  // descr .2, status .3
	OIDPrtAlert         = "1.3.6.1.2.1.43.18.1.1" // severity .2, code .7, descr .8
	OIDPrtConsoleBuffer = "1.3.6.1.2.1.43.16.5.1.2"
)

// Column numbers (appended to table roots with ".").
const (
	colHrDevType, colHrDevDescr, colHrDevStatus = 2, 3, 5
	colHrPrtStatus, colHrPrtErrState            = 1, 2
	colGenSerial                                = 17
	colSupColorantIdx, colSupType, colSupDescr  = 4, 5, 6
	colSupUnit, colSupMax, colSupLevel          = 7, 8, 9
	colInMax, colInCur, colInStatus             = 9, 10, 11
	colInMedia, colInName                       = 12, 13
	colCoverDescr, colCoverStatus               = 2, 3
	colAlertSeverity, colAlertCode, colAlertMsg = 2, 7, 8
)

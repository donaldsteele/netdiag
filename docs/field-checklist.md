# Field validation checklist (T10)

Run this on real hardware before handing out a release. Record the build
version (`netdiag version`), the date, the tester, and the router and printer
models in the table at the bottom.

Status: **NOT YET EXECUTED.** All items are unchecked. They must be completed
by a person on physical hardware. Do not tick an item without observing it.

Machines required:
- [ ] One Windows 11 PC (amd64, standard user account, no admin rights)
- [ ] One Mac with Apple Silicon (darwin-arm64, standard user account)
- [ ] A real event-style router or access point
- [ ] At least one network printer with SNMP enabled and a web page

## 1. Build and packaging

- [ ] `bash scripts/build.sh` completes without errors
- [ ] `ls dist/` lists `netdiag-windows-amd64.zip`, `netdiag-windows-arm64.zip`, `netdiag-darwin-amd64.zip`, `netdiag-darwin-arm64.zip`
- [ ] Each zip contains the binary, `README.md` and `HOW-TO-OPEN.txt`
- [ ] Windows zip extracts and `netdiag.exe version` prints the expected version
- [ ] Mac zip extracts and `./netdiag version` prints the expected version after the `xattr` step in HOW-TO-OPEN.txt

## 2. Windows 11 (standard user, no admin)

- [ ] SmartScreen flow in HOW-TO-OPEN.txt ("More info" > "Run anyway") starts netdiag
- [ ] Browser opens to `http://127.0.0.1:<port>` automatically
- [ ] ICMP sweep finds the router and printer without an elevation prompt
- [ ] A device that blocks ping (for example Windows Firewall on another PC) still appears through ARP
- [ ] Printer shows model, supply levels (text percent, not colour only) and alerts when SNMP is on
- [ ] Printer with SNMP off is still flagged as a printer via port 9100, 631 or 515
- [ ] Web links open the device page in a new tab
- [ ] A /24 scan finishes in under 10 seconds
- [ ] Client isolation warning appears when only the gateway responds (use a guest or isolated network if available)
- [ ] JSON and CSV export download and open correctly
- [ ] Quit button stops the process; Ctrl+C in the console also stops it

## 3. macOS Apple Silicon (standard user, no admin)

- [ ] Gatekeeper steps in HOW-TO-OPEN.txt (`xattr -d com.apple.quarantine netdiag`, `chmod +x`) let `./netdiag` start
- [ ] Browser opens to the local URL
- [ ] ICMP sweep works without `sudo` (unprivileged UDP ICMP)
- [ ] ARP harvest adds hosts that drop ICMP
- [ ] Printer supplies, web links, and export match the Windows results on the same network
- [ ] A /24 scan finishes in under 10 seconds
- [ ] Quit button and Ctrl+C both stop the process

## 4. Edge behaviour

- [ ] Range larger than /22 is refused with the message from README.md (exit code 2 on the CLI)
- [ ] `netdiag ui --port 80` and `--port 443` are refused with exit code 2
- [ ] Scan cancelled mid-run prints partial results and exits with code 1
- [ ] Starting a second scan while one is running is refused (API returns 409; UI shows the refusal)
- [ ] Two netdiag windows at once each get their own random port and both work

## Results

| Date | Tester | Build version | Machine / OS | Router model | Printer model | Notes |
|------|--------|---------------|--------------|--------------|---------------|-------|
|      |        |               |              |              |               |       |

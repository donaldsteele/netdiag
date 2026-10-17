# Field validation checklist (T10)

Run this on real hardware before handing out a release. Record the build
version (`netdiag version`), the date, the tester, and the router and printer
models in the table at the bottom.

Status: **NOT YET EXECUTED.** All items are unchecked. They must be completed
by a person on physical hardware. Do not tick an item without observing it.

Machines required:
- [ ] One Windows 11 PC (amd64, standard user account, no admin rights)
- [ ] One Mac with Apple Silicon (darwin-arm64, standard user account)
- [ ] A real consumer or small-business router or access point
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

## 5. v0.2 features (unchecked; field test deferred)

Tick only after observing the behaviour on real hardware.

### Health panel
- [ ] Gateway row shows `ok` with a latency; unplugging the router shows `fail` text, not colour alone
- [ ] DHCP row shows `ok` on a DHCP lease and `fail` on a 169.254.x.x (APIPA) address
- [ ] DNS row resolves the internet test name (connectivitycheck.gstatic.com, or the internet check host) and shows a latency
- [ ] On a local network without internet, the DNS row is `warn` (not `fail`) and names the DNS server that answers, while the router's DNS server is reachable; it is `fail` when no DNS server answers
- [ ] Web server row shows HTTP status, latency and the must-contain result, once a URL is set in Settings
- [ ] Web server row shows "not configured" (info) when no URL is set
- [ ] Internet row is `info` (never `fail`) on an offline local network
- [ ] Wi-Fi row shows SSID, signal and channel on Wi-Fi; "not on Wi-Fi" on a cable
- [ ] Wi-Fi and DHCP rows show real data (not "unavailable") on a non-English Windows install
- [ ] macOS Wi-Fi row shows "hidden by macOS" without Location permission
- [ ] Recheck finishes within about 10 seconds
- [ ] Header summary chip reads "Health: N fail, M warn" (or "Health: OK")

### Conflicts
- [ ] Two devices given the same static IP produce a MAC flap conflict after two alternations within 10 minutes
- [ ] A single DHCP address change does not produce a conflict
- [ ] A Wi-Fi range extender or mesh node shows the "same MAC on several IPs" warning with the extender hint
- [ ] "Ignore this MAC" removes the conflict and the MAC appears in Settings under ignored MACs
- [ ] Gateway and this computer are never reported as multi-IP conflicts

### mDNS and IPP
- [ ] Printer shows its Bonjour name under the IP address
- [ ] A device that blocks ping but advertises a service appears through mDNS
- [ ] With no mDNS responders the scan shows "mdns: no responses" and completes
- [ ] Printer with port 631 open shows state and supplies "via IPP" when SNMP is off
- [ ] Printer with SNMP and IPP both working shows "via SNMP + IPP" with SNMP values preferred
- [ ] Windows Firewall prompt (private networks) and macOS incoming-connection prompt are answered as described in HOW-TO-OPEN.txt, and the scan still works after Deny

### Live alerts
- [ ] Unplugging a printer in live mode shows a critical banner and a beep (printerOffline)
- [ ] Reloading the page with existing alerts does not beep or notify again (replay is silent)
- [ ] Mute and Unmute change the beep and survive a restart
- [ ] "Enable desktop alerts" from a click shows the browser permission prompt; denying it still leaves the banner and beep working
- [ ] The same alert does not repeat within the cooldown (default 300 s)
- [ ] `netdiag scan --live` prints `ALERT` lines to stdout and the health summary to stderr

### History and compare
- [ ] A completed scan appears in the History tab without any action
- [ ] Live mode writes a checkpoint at about 5 minutes and when stopped
- [ ] Only the newest 20 scans are kept
- [ ] Compare with the default baseline lists added, removed and changed devices correctly
- [ ] JSON and CSV compare exports download with the `netdiag-diff-<timestamp>` name
- [ ] Deleting a history entry removes it from the list and from disk
- [ ] `netdiag compare` exits 2 when both IDs are the same and 1 when there is no earlier scan
- [ ] A corrupt file placed in the history folder is skipped and does not stop the UI

### Data location
- [ ] Settings and history are created under `%AppData%\netdiag` (Windows) or `~/Library/Application Support/netdiag` (macOS)
- [ ] `NETDIAG_DATA_DIR` moves the data to the chosen folder
- [ ] Deleting the folder while netdiag is closed resets everything to defaults on the next start

## Results

| Date | Tester | Build version | Machine / OS | Router model | Printer model | Notes |
|------|--------|---------------|--------------|--------------|---------------|-------|
|      |        |               |              |              |               |       |

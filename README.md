# netdiag

netdiag is a single-binary network diagnostic tool for event staff. It discovers devices on the local subnet (ICMP, ARP, port probes, SNMP) and reports printers, web services and their health through a local browser UI.

It answers, within about 10 seconds: what is alive on the network, which devices are printers and what state they are in (toner, paper, alerts), and which devices offer a web page you can open.

First time? Read **HOW-TO-OPEN.txt** (included in your zip). It explains the SmartScreen (Windows) and Gatekeeper (Mac) steps.

## Quickstart

1. Plug into the event network (router Wi-Fi or cable).
2. Unzip the file for your computer and start `netdiag` (double-click `netdiag.exe` on Windows, or `./netdiag` in Terminal on Mac).
3. Your browser opens to the netdiag page on this computer only.
4. Check the interface and address range (pre-filled). Click **Scan**.
5. Read the results:
   - **Printers** tab: model, toner/ink and tray levels, alerts.
   - **Web** tab: click a link to open that device's web page in a new tab.
   - **Problems** tab: printers with alerts or low/empty supplies, and web services that did not respond.
6. Export with **JSON** or **CSV** if you need to send the results to someone.
7. Click **Quit** when finished (or press Ctrl+C in the Terminal or console window).

Nothing is installed. No administrator rights are needed. The tool only listens on `127.0.0.1` (this computer), with no login, and it never talks to the internet.

## What it checks

- **Ping sweep (ICMP)** of every address in the range, in parallel.
- **ARP table**, which finds devices that block ping (Windows firewall often does this).
- **Port probes** on 80, 443, 8080, 8443 (web), 9100, 631 and 515 (printing). For web ports it reads the page title, server name and status. For HTTPS it reads the certificate. Self-signed or expired certificates are normal on device pages and are reported, not treated as errors.
- **SNMP v2c** (falling back to v1) read-only queries using the community string (default `public`): system name and description, printer model, serial, page count, supplies, trays, covers, alerts. netdiag never changes device settings.
- The **gateway (router)** and **this computer** are labelled.

Scans are limited to ranges of /22 (1024 addresses) or smaller. A /24 typically finishes in under 10 seconds.

## Live mode

Leave **Live mode** ticked (next to **Scan**; your choice is remembered) to keep watching the network after the first scan. Every 15 seconds netdiag checks the addresses where nothing has been found yet. Devices it already knows are never probed again (no port scan, web, SNMP or name lookup); they only get one quick "are you still there?" check, so live mode stays light on the network.

- The status line reads `Live · watching N addresses · last check Xs ago · M new · K offline`.
- A newly arrived device gets a **NEW** badge and a highlighted row, is listed under the **New** tab, and screen readers announce it.
- Each new device is fully checked once (ports, web page, SNMP/printer details).
- Export JSON/CSV at any time; it includes everything found so far.
- Click **Stop live** to stop watching. Starting another scan also stops live mode.

**Offline detection.** Each known device gets one cheap presence check per cycle: a single ping if it was found by ping, otherwise a connection to its lowest open port, otherwise a look at the ARP table (same IP and MAC address). A device that answered ping but stops can still count as present through its open port. After two missed checks in a row a device is marked **OFFLINE** (dimmed row, "offline for 2m", listed under the **Offline** and **Problems** tabs, and announced to screen readers); one missed check never flips it. When it answers again it is shown as back online. If an address suddenly shows a different MAC address (another device got the same IP), it is treated as a new device and fully checked once. Your own computer is never checked. Exports include `online` and `last_seen`.

Live mode does not notice changes on devices it already found (for example toner running low). Run a fresh scan for that.

On the command line: `netdiag scan --live` (optional `--interval 30s`, between 5s and 5m) prints the first results, then one line per new device and one line whenever a device goes offline or comes back, until you press Ctrl+C. With `--json` the first result is a JSON object followed by one JSON line per new device and one `{"type":"presence",...}` line per online/offline change.

## Using the command line

The same engine runs without the browser, for remote support or scripting:

```
netdiag scan                                  # suggested subnet, results as a table
netdiag scan --cidr 192.168.1.0/24 --json     # JSON to stdout (logs to stderr)
netdiag scan --cidr 192.168.1.10-192.168.1.50 --csv report.csv
netdiag scan --cidr 192.168.1.0/24 --community private --timeout 90s
netdiag interfaces                            # list candidate network adapters
netdiag ui --no-browser                       # start the UI without opening a browser
netdiag ui --port 8765                        # fixed port (80 and 443 are refused)
netdiag version
```

Exit codes: `0` success, `1` runtime error (including a scan that was cancelled or timed out, which still prints partial results), `2` invalid arguments (for example a range bigger than /22).

Ranges can be CIDR (`192.168.1.0/24`), a dash range (`192.168.1.10-192.168.1.50`) or a single address.

## Troubleshooting

**Only the router (gateway) responds, or only a few devices show up.**
Wi-Fi "client isolation" (sometimes called AP isolation or guest mode) stops devices from seeing each other. netdiag shows this warning when only the gateway answers. Connect to the event's main network (not a guest network), or ask the venue to turn off client isolation. A wired connection to the event switch usually avoids the problem.

**The printer is found but has no supply or model details.**
SNMP is probably disabled on the printer, or it uses a community string other than `public`. Enable SNMP in the printer's web page (or its front-panel network menu) and set the read community to the event's value, then type that value in the **Community** box and scan again. The printer is still flagged as a printer because its printing ports (9100, 631, 515) are open.

**A device is missing even though you know it is on.**
Some devices block ping. netdiag also reads the ARP table, so a device that has talked to this computer recently should appear. Ask the device to send traffic (open its web page or print a test page), then scan again. Check that you chose the right network in the interface list. The wrong adapter (VPN, virtual machine, Docker) can scan an empty subnet.

**"target range too large (max /22, 1024 hosts)" or the note "subnet truncated".**
netdiag scans at most 1024 addresses (a /22). A bigger range is refused. The automatic suggestion is limited to the /24 around this computer, and you will see a note saying so. Enter a smaller range, or a larger one within /22, in the address box.

**"server: ... address already in use" when starting.**
Another copy of netdiag may be open, or the port you asked for is taken. Close the other window, or start without `--port` so a free port is chosen.

**Windows says "Windows protected your PC" or antivirus quarantines the file.**
netdiag is not code-signed. In SmartScreen choose "More info", then "Run anyway". If antivirus removes it, restore it and add the folder as an exception, or ask IT staff to allow it.

**Mac says the app cannot be opened.**
Run `xattr -d com.apple.quarantine netdiag` in the folder where you unzipped it, then `./netdiag`. See HOW-TO-OPEN.txt.

**The page does not load after the window opens.**
Keep the black window open. Closing it stops netdiag. If the browser did not open, copy the address shown in the window (it begins with `http://127.0.0.1:`) into your browser.

## Limits (by design)

- IPv4 only. No IPv6 scanning.
- SNMP v2c and v1 only. No SNMPv3, no writes or configuration changes.
- Only the seven fixed ports above are probed. netdiag is not a port scanner or vulnerability tool.
- Scan results are kept in memory for the last 10 scans and are lost when netdiag quits. Export before quitting.
- Built and tested for Windows and macOS. Linux is not supported.

## Building from source

Requires Go 1.26.9 or newer (go.mod pins `toolchain go1.26.9`; the Go tool downloads it automatically). From the repository root:

```
bash scripts/build.sh      # writes dist/netdiag-<os>-<arch>.zip for all four targets
```

Set `VERSION=1.2.0` to stamp a version; otherwise `git describe` is used. Each zip contains the binary, this README and HOW-TO-OPEN.txt.

Refresh the vendor database with `bash scripts/update-oui.sh` (needs network access).

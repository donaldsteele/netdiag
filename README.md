# netdiag

netdiag is a single-binary network diagnostic tool for event staff. It discovers devices on the local subnet (ICMP, ARP, port probes, SNMP, mDNS, IPP) and reports printers, web services and the health of the event network through a local browser UI.

It answers, within about 10 seconds: what is alive on the network, which devices are printers and what state they are in (toner, paper, alerts), which devices offer a web page you can open, whether the gateway, DHCP, DNS and the event web server are working, and whether two devices are fighting over the same IP address.

First time? Read **HOW-TO-OPEN.txt** (included in your zip). It explains the SmartScreen (Windows) and Gatekeeper (Mac) steps.

## Quickstart

1. Plug into the event network (router Wi-Fi or cable).
2. Unzip the file for your computer and start `netdiag` (double-click `netdiag.exe` on Windows, or `./netdiag` in Terminal on Mac).
3. Your browser opens to the netdiag page on this computer only.
4. Check the interface and address range (pre-filled). Click **Scan**.
5. Read the results:
   - **Devices** (the default view): the stat tiles (Alive, Printers, Web, mDNS, Offline, Conflicts). Click a tile to filter the table to those devices. Below them is the health strip (gateway, DHCP, DNS, internet, Wi-Fi and event web server) and the hosts table (see below).
   - **Health** (sidebar, or click a health pill): one card per check, with its status, detail and response time (see **Health panel** below).
   - **Activity**: the live activity log of the scan and of live-mode events (see below).
   - **History**: earlier scans, and a **Compare** view that shows what changed between two of them.
   - Click the IP address of a device (or anywhere on its row) to open its **details drawer**: printer model, supplies, SNMP, web links, conflicts and mDNS, with copy buttons for the IP and MAC.
6. Export with **JSON** or **CSV** (the **Export** menu) if you need to send the results to someone.
7. Click **Quit** when finished (or press Ctrl+C in the Terminal or console window).

![netdiag Devices view in the dark theme](docs/img/devices-dark.png)

### The web interface

- **Sidebar navigation**: Devices, Health, Activity, History and Settings. The sidebar becomes a narrow icon rail on medium-width windows and a **menu** button on narrow ones (phones and small windows). Keyboard users can reach every item with Tab, and the current view is marked.
- **Top bar**: the interface, target range and SNMP community fields, the **Live mode** switch, **Scan** / **Stop live**, **Export** and **Quit**, then the health summary and icon buttons for muting alerts (bell), enabling desktop alerts (screen) and switching theme (moon/sun). Hover an icon to see its name. The **LIVE** indicator pulses while live mode runs.
- **Dark and light themes**: the dark theme is the default. Use the theme button in the top bar to switch; your choice is remembered in this browser only. Both themes meet the WCAG AA contrast levels for text and controls.
- **Details drawer**: opens on the right with a full description of one device. It stays open while live mode updates the device, and closes with **Esc** or its close button, which also returns the keyboard focus to the row. On narrow screens it fills the window.
- **Activity log**: a terminal-style feed of the scan (start, stages, finish), new, offline and back-online devices, conflicts, health changes, notices and errors, with the time of each entry. Filter it by kind with the chips at the top, and use **Copy** to put the visible lines on the clipboard. It keeps the most recent 500 entries. It is cleared when you reload the page, and is not saved to disk.
- **Motion**: the LIVE indicator, new-device highlights and the tile counts animate briefly. If your computer is set to reduce motion, all animation is switched off.
- **Keyboard**: Tab and Shift+Tab move between controls, Enter opens a row's details, and Esc closes the drawer and the settings dialog. Every control has a visible focus ring.
- **Fonts and offline use**: the Inter and JetBrains Mono fonts are included in the program, so the page looks the same without an internet connection, and makes no outside requests.

![netdiag details drawer in the light theme](docs/img/drawer-light.png)

Nothing is installed. No administrator rights are needed. The tool only listens on `127.0.0.1` (this computer), with no login. The web page itself makes no requests to the internet. The only outside request netdiag makes is the optional **internet check** (one quick request to a test address, described under Health panel), which runs from this computer.

## What it checks

- **Ping sweep (ICMP)** of every address in the range, in parallel.
- **ARP table**, which finds devices that block ping (Windows firewall often does this).
- **Port probes** on 80, 443, 8080, 8443 (web), 9100, 631 and 515 (printing). For web ports it reads the page title, server name and status. For HTTPS it reads the certificate. Self-signed or expired certificates are normal on device pages and are reported, not treated as errors.
- **SNMP v2c** (falling back to v1) read-only queries using the community string (default `public`): system name and description, printer model, serial, page count, supplies, trays, covers, alerts. netdiag never changes device settings.
- **mDNS (Bonjour)** browse on the chosen network: names such as "Brother HL-L2350DW" and the services a device advertises (printing, web, AirPlay and so on). This also finds devices that block ping. See **Printer discovery** below.
- **IPP** (port 631): for printers that answer, the printer's state and supply levels are read when SNMP is off or incomplete. Read-only.
- **Health checks** on the event network (see **Health panel**).
- The **gateway (router)** and **this computer** are labelled.

Scans are limited to ranges of /22 (1024 addresses) or smaller. A /24 typically finishes in under 10 seconds.

## Health panel

The health panel sits above the hosts table. Each row shows an icon, a text status, the check name, a short detail and, where it applies, the response time. Status is always written out in words, never only by colour. Click **Recheck** to run the checks again now (they finish within about 10 seconds). The header shows a summary such as "Health: 1 fail, 2 warn".

| Check | What it means |
|---|---|
| **Gateway reachable** | The router answers a ping. A fail means this computer cannot reach the router at all. |
| **DHCP lease** | Whether this computer got its address from DHCP (normal) or is using a static address. An address in the 169.254.x.x range (APIPA) means DHCP failed, and the check reports `fail`. |
| **DNS resolving** | Whether names such as the event web server can be looked up. |
| **Event web server** | Your event's own web page (set the address in **Settings**, "Web server URL"). netdiag reports the HTTP status, the response time and, if you set one, whether the page contains your "must contain" text. If you have not set a URL the check shows "not configured" (info, not a fault). |
| **Internet reachability** | Information only. It asks a test address (`http://connectivitycheck.gstatic.com/generate_204` by default, changeable in Settings) whether the internet is reachable. It never shows `fail`, because an offline event network is normal. |
| **Wi-Fi** | SSID (network name), signal strength, channel, band and link speed, when the computer is on Wi-Fi. A wired computer shows "not on Wi-Fi" (info). On Windows in a non-English language, or when the details cannot be read, it shows "unavailable". On a Mac, macOS can hide the network name unless the app has Location permission; netdiag then shows "hidden by macOS". |

Statuses: `ok` (fine), `warn` (worth a look), `fail` (broken), `info` (for information, including "not configured" and "not on Wi-Fi"), and `unavailable` (netdiag could not read this check, with the reason in the detail). A health problem never stops a scan.

## Conflicts

netdiag watches the address table and the names of devices during a scan and in live mode. It flags three kinds of problem:

- **MAC flapping**: one IP address keeps switching between two MAC (hardware) addresses. This usually means two devices have the same IP. A single change is normal (a device got a new address from DHCP) and is not flagged; the conflict is only raised when the address switches back and forth more than once within 10 minutes.
- **Same MAC on several IPs**: one hardware address answers for several IP addresses. The gateway and this computer are excluded.
- **Duplicate name**: two devices with different MAC addresses report the same hostname, mDNS name or SNMP system name.

Conflicts show as a red or amber **CONFLICT** label on the affected rows, are listed by the **Conflicts** tile and filter on the Devices view (and under **Problems**), and get a section in the detail panel explaining the kind of conflict.

**Wi-Fi range extenders and mesh nodes.** Many extenders and mesh nodes answer ARP for every device they relay, so several IP addresses share one MAC address. netdiag reports this as a warning with this hint: *if this is an extender or mesh node, use Ignore this MAC.* Click **Ignore this MAC** in the detail panel to stop reporting that MAC. The ignored MAC is saved in Settings under "Ignored MACs" (you can remove it again there). While a MAC is ignored, netdiag raises no conflict for it (flapping, multiple IPs or duplicate name); the devices themselves still appear in the list.

## Printer discovery (mDNS and IPP)

- **mDNS** finds devices that announce themselves on the local network. netdiag sends its questions from an ephemeral (random) UDP port and does not take over the standard mDNS port, so it does not disturb macOS or Windows. The device's Bonjour name is shown under its IP address, and the services it advertises appear in the detail panel. A device that only shows up through mDNS is marked "mdns service _ipp._tcp" (or similar) as the reason it is treated as a printer.
- **IPP** asks a printer that has port 631 open for its state (idle, processing, stopped), its state reasons (for example "media empty") and its supply levels. The printer's source tag reads **via IPP**, **via SNMP** or **via SNMP + IPP** (both were used, with SNMP values taking priority).
- If nothing answers mDNS, the scan shows the note "mdns: no responses". This is information only and the scan carries on. A firewall (Windows Defender Firewall, or a VPN client) can block mDNS, and some guest networks do not forward it.
- netdiag uses the network you chose on the first screen. If you have more than one adapter (for example Wi-Fi and a docking station), pick the one connected to the event network.

## Alerts (live mode)

In live mode netdiag raises an alert when something changes: a printer goes offline, a printer reports an alert, a supply runs low, a new device appears, an IP conflict is found, or a health check starts failing. Alerts appear as a banner stack at the top of the page (up to three at a time, with "N more" for the rest; close each with its button). Critical alerts are announced at once to screen readers; others are announced politely. Each alert makes a beep, with a different tone for critical and warning alerts.

- The **bell** button in the top bar mutes or unmutes the beep (a crossed-out bell means muted). The choice is saved in Settings.
- The **screen** button (**Enable desktop alerts**) in the top bar shows a desktop notification too. Your browser asks for permission when you click the button (it must be a click, so the browser allows the prompt). If you say no, the banner and beep still work and netdiag shows a short note. To change your mind later, use the browser's site settings for the 127.0.0.1 address.
- Alerts only fire when something changes, not on every check, and the same alert is not repeated within its **cooldown** (default 300 seconds). Opening the page again does not replay old alerts with sound.
- **Settings** (header button) has the switches for each alert type, the **supply threshold** (percent, 1 to 100, default 15) and the **cooldown** (30 to 3600 seconds). "Device offline" is off by default because it is noisy; "Printer offline" is on.
- On the command line, `netdiag scan --live` prints each alert as a line (`ALERT ...`, or JSON lines with `--json`) and the health summary to stderr.

## History and compare

Every completed scan is saved automatically, and live mode saves a checkpoint every five minutes and again when you stop it. netdiag keeps the **20 most recent** scans.

- **History** view: the list of saved scans, with the date, the range, the device counts, a **live** badge for live-mode scans and a **Delete** button for each one.
- **Compare**: pick a baseline (the earlier scan; netdiag selects the previous scan of the same range by default) and the current scan. The result lists devices **Added**, **Removed** and **Changed** (new IP or MAC address, open ports, printer state and supply changes, online/offline, web port changes), with from-to text for each change. Export the comparison as JSON or CSV with the export buttons.
- Devices are matched by MAC address when both scans have one, otherwise by IP address.

On the command line: `netdiag history` lists the saved scans, and `netdiag compare [--base ID] [--current ID] [--json | --csv FILE]` compares two of them (see below).

**Where the data is kept.** Settings and history are saved in one folder per user, never in the program's own folder:

| System | Folder |
|---|---|
| Windows | `%AppData%\netdiag` (for example `C:\Users\<name>\AppData\Roaming\netdiag`) |
| macOS | `~/Library/Application Support/netdiag` |

The folder holds `settings.json` (your settings) and a `history` folder with one file per saved scan. To change the folder for one run, set the environment variable `NETDIAG_DATA_DIR` to the folder you want before starting netdiag (for example `set NETDIAG_DATA_DIR=D:\netdiag-data` in Command Prompt, or `$env:NETDIAG_DATA_DIR="D:\netdiag-data"` in PowerShell, or `NETDIAG_DATA_DIR=/path ./netdiag` in Terminal).

**How to clear it.** To remove one scan, use **Delete** in the History view. To reset your settings to the defaults, quit netdiag and delete `settings.json`. To clear everything, quit netdiag and delete the whole folder above (or the `NETDIAG_DATA_DIR` folder you chose). netdiag creates the folder again the next time it starts.

## Using the command line

## Live mode

Leave **Live mode** ticked (next to **Scan**; your choice is remembered) to keep watching the network after the first scan. Every 15 seconds netdiag checks the addresses where nothing has been found yet. Devices it already knows are never probed again (no port scan, web, SNMP or name lookup); they only get one quick "are you still there?" check, so live mode stays light on the network.

- The status line reads `Live · watching N addresses · last check Xs ago · M new · K offline`.
- A newly arrived device gets a **NEW** badge and a highlighted row, is listed under the **New** filter, and screen readers announce it.
- Each new device is fully checked once (ports, web page, SNMP/printer details).
- Export JSON/CSV at any time; it includes everything found so far.
- Click **Stop live** to stop watching. Starting another scan also stops live mode.

**Offline detection.** Each known device gets one cheap presence check per cycle: a single ping if it was found by ping, otherwise a connection to its lowest open port, otherwise a look at the ARP table (same IP and MAC address). A device that answered ping but stops can still count as present through its open port. After two missed checks in a row a device is marked **OFFLINE** (dimmed row, "offline for 2m", listed under the **Offline** and **Problems** filters, and announced to screen readers); one missed check never flips it. When it answers again it is shown as back online. If an address suddenly shows a different MAC address (another device got the same IP), it is treated as a new device and fully checked once. Your own computer is never checked. Exports include `online` and `last_seen`.

Live mode does not notice changes on devices it already found (for example toner running low). Run a fresh scan for that.

On the command line: `netdiag scan --live` (optional `--interval 30s`, between 5s and 5m) prints the first results, then one line per new device and one line whenever a device goes offline or comes back, until you press Ctrl+C. With `--json` the first result is a JSON object followed by one JSON line per new device and one `{"type":"presence",...}` line per online/offline change.

## Using the command line

The same engine runs without the browser, for remote support or scripting:

```
netdiag scan                                  # suggested subnet, results as a table
netdiag scan --cidr 192.168.1.0/24 --json     # JSON to stdout (logs to stderr)
netdiag scan --cidr 192.168.1.10-192.168.1.50 --csv report.csv
netdiag scan --cidr 192.168.1.0/24 --community private --timeout 90s
netdiag scan --live --interval 30s            # keep watching; prints ALERT lines and new devices
netdiag history                               # table of saved scans (IDs, dates, ranges)
netdiag compare                               # newest scan vs the previous scan of the same range
netdiag compare --base ID --current ID --csv diff.csv
netdiag interfaces                            # list candidate network adapters
netdiag ui --no-browser                       # start the UI without opening a browser
netdiag ui --port 8765                        # fixed port (80 and 443 are refused)
netdiag version
```

`compare` exits `2` when the two IDs are the same, and `1` when a scan is missing or there is no earlier scan to compare with. Use `--csv -` to print the CSV to stdout (not together with `--json`).

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

**The health panel says "not configured" for the event web server.**
That is information, not a fault. Open **Settings** and enter the event web server address in "Web server URL" (and optionally the text the page must contain). Then press **Recheck**.

**The health panel says "not on Wi-Fi" or "unavailable" for Wi-Fi.**
"Not on Wi-Fi" means this computer is on a cable (or Wi-Fi is off), which is fine. "Unavailable" means the Wi-Fi details could not be read; on Windows this happens with a non-English language setting. The rest of the scan is not affected.

**The health panel says "self-assigned 169.254.x.x address" (DHCP fail).**
This computer did not get an address from the router. Check the cable or Wi-Fi connection, and restart the network connection or the computer. Ask the venue to check the DHCP server if it keeps happening.

**"mdns: no responses" in the scan notes.**
No device answered the Bonjour (mDNS) query. Devices may still be found through ping, ARP and ports. Check that the firewall is not blocking netdiag, and that you picked the adapter connected to the event network.

**A "CONFLICT" appears on a device that is fine (for example a Wi-Fi extender).**
Click **Ignore this MAC** in the detail panel. The MAC is saved in Settings.

**The page does not load after the window opens.**
Keep the black window open. Closing it stops netdiag. If the browser did not open, copy the address shown in the window (it begins with `http://127.0.0.1:`) into your browser.

## Limits (by design)

- IPv4 only. No IPv6 scanning.
- SNMP v2c and v1 only. No SNMPv3, no writes or configuration changes.
- Only the seven fixed ports above are probed. netdiag is not a port scanner or vulnerability tool.
- The last 20 scans are saved on this computer (see **History and compare**). Export anything you need to send to someone; the saved files are only on this computer.
- The only request netdiag makes to the internet is the optional internet check (a single request, server-side, configurable in Settings). The web page makes no external requests.
- Built and tested for Windows and macOS. Linux is not supported.

## Building from source

Requires Go 1.26.9 or newer (go.mod pins `toolchain go1.26.9`; the Go tool downloads it automatically). From the repository root:

```
bash scripts/build.sh      # writes dist/netdiag-<os>-<arch>.zip for all four targets
pwsh scripts/build.ps1     # same, from PowerShell on Windows
```

Set `VERSION=1.2.0` to stamp a version; otherwise `git describe` is used. Each zip contains the binary, this README and HOW-TO-OPEN.txt.

Refresh the vendor database with `bash scripts/update-oui.sh` (needs network access).

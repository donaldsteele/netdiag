# Field validation checklist (T10)

Run this on real hardware before handing out a release. Record the build
version (`netdiag version`), the date, the tester, and the router and printer
models in the table at the bottom.

Status: **NOT YET EXECUTED.** All items are unchecked. They must be completed
by a person on physical hardware. Do not tick an item without observing it.
The v0.6 items (section 6) were added on 2026-10-18 and are not run yet; tick
each one only with its date, OS and result filled in.

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
- [ ] Starting netdiag a second time opens the page of the one already running (v0.6, see section 6); a second netdiag started with its own `NETDIAG_DATA_DIR` gets its own port and both work

## 5. v0.2 features (unchecked; field test deferred)

Tick only after observing the behaviour on real hardware.

### Remote access (v0.5.7)
- [ ] A Windows PC with Remote Desktop on shows port 3389 and a **Remote desktop** link in the Web / remote column
- [ ] On Windows, clicking the link (or **Open remote desktop** in the drawer) opens Remote Desktop Connection to that address, with no other prompt from netdiag
- [ ] On macOS with Windows App installed, **Open remote desktop** opens Windows App to that address; without it, the drawer says to install it and **Download connection file** works
- [ ] A Mac with Screen Sharing on (or a VNC server) shows port 5900; on macOS **Open screen sharing** opens the built-in Screen Sharing app; on Windows without a VNC viewer only **Copy address** is offered
- [ ] A device with SSH shows **Copy SSH command** (copies `ssh <ip>`) and no open button
- [ ] In live mode, a Linux machine with SSH open and other ports open logs no SSH connection every cycle (`journalctl -u ssh` / `/var/log/auth.log`)
- [ ] After a full /24 scan, 30 minutes of live mode and opening Remote Desktop from netdiag, Microsoft Defender Protection history shows nothing for netdiag
- [ ] A Windows PC with only Remote Desktop open (firewall blocking ping) stays online in live mode, and its Remote Desktop log (Event Viewer > RemoteDesktopServices-RdpCoreTS) shows no connection every cycle
- [ ] The "this computer" row never offers Open remote desktop, even when this PC has Remote Desktop on

### Data safety (v0.5.8)
- [ ] With a non-default SNMP community in Settings (for example `private` on a printer set to it), a live scan still shows the printer's SNMP details and its status keeps updating
- [ ] The same scan's JSON export, its file in the `history` folder, and `netdiag scan --json` output show the community as `***`; only `settings.json` and the Settings screen show the real value
- [ ] A history file saved by v0.5.7 (community in clear) shows `***` in the exported JSON of that scan, and after opening **History** once the file itself shows `"community":"***"`; the scan still opens and compares as before
- [ ] After quitting and starting netdiag again, `/api/scans/<id>/export?format=csv` for a scan in History downloads it
- [ ] With the supply threshold set above a real printer's lowest toner level (for example 50% for toner at 40%), the printer is listed under **Problems**; at a threshold below the level it is not
- [ ] A live scan left running overnight on a busy network still alerts again for a device that goes offline after the cooldown has passed

### Health checks (v0.5.8)
- [ ] With ping to the router getting no reply, the Router row is `ok` and says "answers on port 80" (or 443), with "(it does not reply to ping)" or, when this computer blocks the ping, "(ping could not run on this computer)". To get there, use a router or access point whose ping reply on the local network side can be turned off (a "WAN ping" setting does not affect pings from the local network), or block outgoing ping (ICMP echo request) to the router's address on the test computer, for example with a Windows Firewall outbound rule. Write down which method you used and the detail shown
- [ ] With the router unplugged, the Router row is `fail` and says it did not reply to ping or on ports 80 and 443, within about 5 seconds
- [ ] A Web server URL starting with `http://` on a server that redirects to `https://` on the same name is `ok` and shows "redirected to https://..."
- [ ] A Web server URL that redirects to a sign-in page on another server is `fail` and the detail names that server; its card names the address with **Use this address**, which fills "Web server URL" in Settings and saves nothing until **Save**
- [ ] A Web server URL on a server that redirects in a loop (or, with the expected answer set to a redirect, one that answers with another redirect) is `fail` with "isn't answering as expected" and offers no address to use
- [ ] A Web server URL with a user name and password (`http://user:password@server/`, on a device that asks for them) is `ok`; the Web server row, the scan export, its `history` file and MCP `get_health` / `get_settings` show the password as `xxxxx`, and only Settings shows it
- [ ] An `https://` Web server URL on a device with a self-signed certificate (a printer, NAS or router admin page) is `fail` with "the certificate is not trusted: self-signed or from a private authority" and a **Trust this certificate** button, on both Windows and macOS
- [ ] The confirmation shows the server address and a fingerprint that matches the one the browser shows for that page (certificate viewer, SHA-256); **Cancel** changes nothing
- [ ] After **OK** the row turns `ok` at once, stays `ok` after quitting and starting netdiag again, and Settings shows "Trusted certificate" with the first part of the fingerprint
- [ ] After the device gets a new certificate (regenerate it in its admin page), the row says "the certificate changed since you trusted it" and offers to trust the new one
- [ ] On a server whose https address redirects to another https port with a different self-signed certificate (for example a NAS that sends `https://nas/` on to `https://nas:5001/`), after trusting the first certificate the row names the second address and says it uses a different certificate that is not trusted, with no **Trust this certificate** button
- [ ] **Forget** in Settings, then **Save**, makes the row fail again without pressing **Check again**
- [ ] After changing the Web server URL in Settings and pressing **Save**, the Web server row describes the new address within about 10 seconds without pressing **Check again**, and **Trust this certificate** is never offered for the old address
- [ ] An `https://` Web server with a normal public certificate is `ok` without any trust step
- [ ] The Web server card's "Set the web server URL in Settings" link shows when no URL is set and opens Settings
- [ ] On a computer with a VPN or a second network adapter (wired and Wi-Fi on different networks), after scanning one network, **Check again** and the MCP `run_health_check` tool check that network: the Router row names its router, and the Wi-Fi and DHCP rows describe that adapter
- [ ] Before any scan, MCP `run_health_check` checks the adapter that carries the default route (the one marked as default in the interface list), not a VPN or virtual adapter
- [ ] On a computer with wired and Wi-Fi on different networks, choose the adapter that does not carry the default route and press **Check again**: the Router row never names the other network's router; when no router on that adapter's network is known it says "no router is known for this network adapter"

### Health panel
- [ ] Router row shows `ok` with a latency; unplugging the router shows `fail` text, not colour alone
- [ ] "Getting an address (DHCP)" row shows `ok` on a DHCP lease and `fail` on a 169.254.x.x (APIPA) address
- [ ] DNS row resolves the internet test name (connectivitycheck.gstatic.com, or the internet check host) and shows a latency
- [ ] On a local network without internet, the "Name lookup (DNS)" row is **Note** (`warn`, not `fail`) and names the DNS server that answers, while the router's DNS server is reachable; it is `fail` when no DNS server answers
- [ ] Web server row shows HTTP status, latency and the must-contain result, once a URL is set in Settings
- [ ] Web server row shows "not configured" (info) when no URL is set
- [ ] Internet row is `info` (never `fail`) on an offline local network
- [ ] Wi-Fi row shows SSID, signal and channel on Wi-Fi; "not on Wi-Fi" on a cable
- [ ] Wi-Fi and DHCP rows show real data (not **Could not run**) on a non-English Windows install
- [ ] macOS Wi-Fi row shows the name as "(name hidden by OS)" without Location permission, or, when macOS shares no Wi-Fi details, says "macOS doesn't share Wi-Fi details with netdiag. The other checks still work."
- [ ] **Check again** finishes within about 10 seconds
- [ ] Header summary chip reads "Health: N failing, M notes" (or "Health: K could not run", or "Health: OK"), with the shape the **Problems right now** card gives the most serious check: ✖ for a failed router, ▲ for another failed check or one that could not run, ⓘ for notes only

### Conflicts
- [ ] Two devices given the same static IP produce a MAC flap conflict after two alternations within 10 minutes
- [ ] A single DHCP address change does not produce a conflict
- [ ] A Wi-Fi range extender or mesh node shows the "same MAC on several IPs" case as a note, not a warning: ℹ **NOTE** on its rows, "Note: One device uses several addresses" in the detail panel with the advice that this is normal for extenders, and "One device uses several addresses" under "Also worth knowing" on the Problems right now card
- [ ] With team chat connected, the alert for two devices given the same static IP names both MAC addresses (the "MACs" field); an extender's "One device uses several addresses" is only sent when **Send** is **Everything, including notes**
- [ ] "Ignore this MAC" asks for confirmation first (Cancel keeps the conflict), then removes the conflict, and the MAC appears in Settings under Ignored conflict MACs
- [ ] Remove next to that MAC in Settings, then Save, brings the conflict back on a later check
- [ ] Gateway and this computer are never reported as multi-IP conflicts

### mDNS and IPP
- [ ] Printer shows its Bonjour name under the IP address
- [ ] A device that blocks ping but advertises a service appears through mDNS
- [ ] With no mDNS responders the scan shows "mdns: no responses" and completes
- [ ] Printer with port 631 open shows state and supplies "via IPP" when SNMP is off
- [ ] Printer with SNMP and IPP both working shows "via SNMP + IPP" with SNMP values preferred
- [ ] macOS incoming-connection prompt is answered as described in HOW-TO-OPEN.txt, and the scan still works after Deny

### Windows Defender Firewall alert (v0.5.8)
Use a Windows 11 standard user account with Windows Defender Firewall on and no rule for netdiag yet (a new folder for netdiag.exe counts as a new program). Note whether the network is Private or Public.
- [ ] Record whether a "Windows Security Alert" appears, and when: at start, at the first scan, or not at all
- [ ] On the standard account, "Allow access" asks for an administrator password, and Cancel closes the alert, as HOW-TO-OPEN.txt says
- [ ] After Cancel: ping, ARP, port probes, SNMP printer details, IPP, web links and all health checks still work (compare with the same scan with the firewall allowing netdiag)
- [ ] After Cancel: record whether Bonjour (mDNS) names still appear, or the scan shows "mdns: no responses"
- [ ] After an administrator clicks "Allow access" (or allows netdiag in Windows Security > Firewall & network protection > Allow an app through firewall), Bonjour names appear
- [ ] Record what Cancel left in Windows Defender Firewall with Advanced Security > Inbound Rules (block rules for netdiag, or none), and whether the alert comes back at the next scan

### Live alerts
- [ ] Unplugging a printer in live mode shows a warning banner and one beep (printerOffline, v0.6); plugging it back in turns that banner into "Fixed: Printer back online" after two good checks, with no beep
- [ ] When the page loses its connection for a moment and reconnects (for example after the PC sleeps and wakes), old alerts do not beep or notify again (replay is silent)
- [ ] Reloading the page during live mode brings the live view back (table, health, live status and banners) without playing a sound, as README "Live mode" says (v0.6); pressing Scan in that page asks **Restart watching?**, and **Keep watching** leaves the live scan running
- [ ] Reloading during the first scan picks that scan up again, and live mode starts when it finishes (v0.6)
- [ ] Mute and Unmute change the beep and survive a restart
- [ ] "Enable desktop alerts" from a click shows the browser permission prompt; denying it still leaves the banner and beep working
- [ ] An alert is not repeated while its problem lasts (a printer that stays offline gives one banner), and the same new device is not announced twice within the cooldown (default 300 s). A problem that was fixed and comes back is shown again at once (v0.6)
- [ ] `netdiag scan --live` prints `ALERT` lines, and `FIXED` lines that say what was fixed, to stdout and the health summary to stderr

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

## 6. v0.6 chat alerts and watching

Run these with the v0.6 release candidate. Each item has the steps to follow
and what you should see. Fill in its **Date**, **OS** and **Result** line
(what you saw, in a few words, plus anything unexpected), then tick it. An
item that fails stays unticked: write what happened in its Result line.

Set up first (v0.6 §3, "Test rig"):
- A Windows 11 PC with a standard user account (no administrator rights) and Windows Defender Firewall on
- A Mac with Apple Silicon on macOS 15, with a standard user account
- A bubbles v0.2.0 server on another computer on the same local network, with its admin page open, and a channel such as #netdiag-alerts
- The Zebra ZD621 at 192.168.68.76, with someone next to it who opens and closes the cover when asked
- For the mDNS checks in 6.4, on the same local network: an iPhone or iPad that is not set up for Wi-Fi sync; a Mac mini, Mac Studio or Mac Pro with neither "MacBook" nor "iMac" in its name and File Sharing off (a second Mac, not the macOS 15 test Mac, so it is "not this computer" on both runs); an Apple TV; and a HomePod
- On both test computers, the user who runs netdiag has never set up the bubbles tray app (6.4, "The connect wizard finds bubbles by its mDNS name.")
- netdiag connected to team chat (6.2, "Connect to team chat without admin") before running 6.1

When testing, stop only the netdiag you started, by its process ID or its path, never by its name (no `taskkill /IM`, no `pkill netdiag`).

### 6.1 Real hardware: the Zebra and team chat (v0.6 §7.4)

- [ ] **Cover open, then closed.** Steps: with team chat connected and **Send alerts to team chat** on, start a live scan of the Zebra's network and wait for the first scan to finish. Ask for the Zebra's cover to be opened. Within about 5 seconds the banner reads "Urgent: Printer alert <name>: Door or cover open", where <name> is how netdiag names the Zebra, for example "ZD621 (192.168.68.76)" (one alert: the printer's "offline or paused" flag is part of it), and #netdiag-alerts gets exactly one alert card within 10 seconds. Ask for the cover to be closed. The banner turns into ✔ **Fixed**, with no sound, and the same chat card (same key) is resolved with "Fixed at HH:MM (lasted N min)" within 10 seconds. Take all five screenshots in this one run, so the site's Zebra story matches: while the cover is open, `real-zebra-fault.png` (the page with the alert banner) and `real-zebra-fault-drawer.png` (the Zebra's details panel with its alert); after it is fixed, `real-zebra-recovered.png` (the Fixed banner) and `real-zebra-activity.png` (Activity with the alert and its Fixed line), all into `docs/assets/shots/`; and a screenshot of the resolved card in bubbles as `docs/assets/shots/bubbles-resolved.png`.
  Date: · OS: · Result:
- [ ] **Printer offline, then back.** Steps: in live mode, ask for the Zebra to be switched off. After 2 missed checks (about 30 seconds at the default 15 s), "Printer offline" appears on the page and in chat, as a warning. Ask for it to be switched on: after 2 good checks the banner turns into "Fixed: Printer back online at HH:MM (lasted N min)" and the chat card is resolved with the same words.
  Date: · OS: · Result:
- [ ] **Restart while a problem is open.** Steps: open the cover and wait for its alert. Click **Quit**. Ask for the cover to be closed. Start netdiag again and scan with **Live mode** ticked: the first scan gives one Fixed and one chat resolve, and no new alert. Then open the cover, restart netdiag the same way with the cover still open: no second alert appears, on the page or in chat.
  Date: · OS: · Result:
- [ ] **This computer loses the network.** Steps: in live mode, unplug this computer's network cable (or turn its Wi-Fi off) for 2 minutes. No device is reported offline; the status line says "Live · checks paused: this computer lost the network", one "Warning: This computer lost the network" notice appears, nothing is posted to chat, and one "netdiag is back on the network" notice appears after reconnecting. Then put the laptop to sleep for 5 minutes and wake it: no devices are reported offline.
  Date: · OS: · Result:
- [ ] **Chat server down.** Steps: stop the bubbles server. Open the cover, then close it, then start the server: nothing about it is posted. Stop the server again, open the cover, click **Quit** in netdiag, start the server, then start netdiag and scan with **Live mode**: exactly one alert is posted, and in bubbles its time is when the cover was opened, not when netdiag started again.
  Date: · OS: · Result:
- [ ] **One netdiag at a time.** Steps: double-click netdiag twice. The second start opens the same page (same address, buttons working) and its window closes by itself, saying "netdiag is already running at …". Stop only the netdiag you started.
  Date: · OS: · Result:

### 6.2 Windows 11, standard user, firewall on (v0.6 §7.5)

- [ ] **First start uses 127.0.0.1:8787.** Steps: on a standard account that never ran netdiag (or with a new, empty `NETDIAG_DATA_DIR`), start netdiag. The browser opens `http://127.0.0.1:8787/`; record which browser.
  Date: · OS: · Result:
- [ ] **A second start opens the same page.** Steps: start netdiag again while it runs. The page that opens is the running one, and its buttons work (Settings opens and saves).
  Date: · OS: · Result:
- [ ] **Buttons work in Edge, Chrome and Firefox.** Steps: make each browser the default in turn and start netdiag. The page's buttons work on the first open: no "Buttons on this page are turned off for safety" banner. Record each browser and version.
  Date: · OS: · Result:
- [ ] **Connect to team chat without admin.** Steps: Settings > Team chat (bubbles) > **Connect to team chat**. The bubbles server is listed (record it if it is not). **Use this**, compare the first four groups of the fingerprint with bubbles Admin > Overview, **It matches**, have the chat admin approve in Admin > Pairing with a channel ticked, then **Send test alert**: the test card shows in the channel and closes. No administrator password is asked for at any point.
  Date: · OS: · Result:
- [ ] **Firewall prompt while looking for the chat server.** Steps: note whether a Windows Security Alert appears while netdiag looks for the chat server. Click **Cancel** if it does, then check that **Type the address** (the address from bubbles Admin > Overview) still connects.
  Date: · OS: · Result:
- [ ] **The chat key is protected (DPAPI).** Steps: after connecting, open `bubbles.json` in the data folder: it holds no readable token (the key is sealed). Copy `bubbles.json` into another Windows user's data folder and start netdiag there: Settings says **Needs reconnecting**, and **Reconnect** works.
  Date: · OS: · Result:
- [ ] **Open Location settings.** Steps: turn Location off (Settings > Privacy & security > Location), start a scan, open Health: the Wi-Fi card says Windows hides the details and **Open Location settings** opens that Settings page, nothing else.
  Date: · OS: · Result:
- [ ] **The chat server moves.** Steps: with team chat connected, give the bubbles server's computer a new address on the local network (for example renew its DHCP lease to a new address, or set another static address) and restart bubbles. Within about 3 minutes netdiag sends alerts again without reconnecting (send a test alert to check).
  Date: · OS: · Result:

### 6.3 macOS 15 (Apple Silicon), standard user (v0.6 §7.5)

- [ ] **Only one netdiag on the Mac.** Steps: start `./netdiag` in Terminal, then again in a second Terminal window. The second one opens the running page and exits.
  Date: · OS: · Result:
- [ ] **The page opens with its buttons working.** Steps: start netdiag from Terminal with Safari as the default browser, then with Chrome. The page's buttons work on the first open (no "Buttons on this page are turned off for safety" banner).
  Date: · OS: · Result:
- [ ] **netdiag finds the chat server.** Steps: Settings > Team chat (bubbles) > **Connect to team chat**: the bubbles server is listed without typing its address. Finish connecting and send a test alert.
  Date: · OS: · Result:
- [ ] **The chat file is private.** Steps: after connecting, run `ls -l ~/Library/Application\ Support/netdiag/bubbles.json`: it reads `-rw-------` (only your user can read it).
  Date: · OS: · Result:
- [ ] **No Location button on a Mac.** Steps: scan, then open Health, also with Location Services off for netdiag (System Settings > Privacy & Security > Location Services). No card shows **Open Location settings**; the Wi-Fi card may show the name as "(name hidden by OS)", or say "macOS doesn't share Wi-Fi details with netdiag. The other checks still work." instead.
  Date: · OS: · Result:
- [ ] **The Mac build.** Steps: on the build machine, `GOOS=darwin go vet ./...` passes and `scripts/build.sh` makes both darwin zips; the darwin-arm64 binary runs on this Mac after the HOW-TO-OPEN.txt steps.
  Date: · OS: · Result:

### 6.4 Windows and macOS

- [ ] **Separate data folders, separate netdiags.** Steps: start netdiag normally, then start a second one with `NETDIAG_DATA_DIR` set to an empty folder. The second one runs on its own port with its own page, and both work.
  Date: · OS: · Result:
- [ ] **Another user's netdiag does not take your page.** Steps: sign in as a second user on the same computer (fast user switching) and start netdiag there first, then start netdiag as the first user. The first user's page is their own netdiag (their settings and history), on another port if 8787 is taken.
  Date: · OS: · Result:
- [ ] **A phone shows as a phone.** Steps: on the local network, have an iPhone or iPad that is not set up for Wi-Fi sync awake with Wi-Fi on (not on a guest network), and scan. Its row's Type reads "Phone/tablet". Open its details panel: the mDNS section lists `_companion-link._tcp`, and the reason under the Type reads "mdns _companion-link". Only that reason passes. If the mDNS section also lists `_apple-mobdev2._tcp` (the reason then reads "mdns _apple-mobdev2"), the run does not count: turn off **Show this iPhone when on Wi-Fi** for it in Finder (or in iTunes on Windows), or use an iPhone or iPad that was never set up for Wi-Fi sync, and scan again. Record the model, its name and the reason. Run it on Windows and on the Mac.
  Date: · OS: · Result:
- [ ] **Macs, Apple TVs and HomePods keep their types.** Steps: have awake on the local network a Mac mini, Mac Studio or Mac Pro that is not this computer, whose name has neither "MacBook" nor "iMac" in it (its default name, such as "Don's Mac mini", is fine), with File Sharing off (System Settings > General > Sharing) and AirPlay Receiver on (the default; System Settings > General > AirDrop & Handoff), plus an Apple TV and a HomePod. Scan. Their mDNS sections may list `_companion-link._tcp`, but none of them reads "Phone/tablet": the Mac reads "Computer", with a reason that names its model (for example "mdns model Mac14,3"), and the Apple TV and the HomePod read "TV/streamer". Then turn AirPlay Receiver off on that Mac and scan again: it still reads "Computer". Record the Mac's model, its name, and whether File Sharing and AirPlay Receiver were on; each device's Type and the reason under it; and any device that is missing. Run it on Windows and on the Mac.
  Date: · OS: · Result:
- [ ] **The connect wizard finds bubbles by its mDNS name.** Steps: on a computer where the bubbles tray app has never been set up (so the wizard cannot read the tray's server), open Settings > Team chat (bubbles) > **Connect to team chat**. The bubbles server on another computer is listed under the chat servers found on the local network, without typing its address and without the "This computer already uses the chat server" line. Then scan the local network: the bubbles server's details panel lists `_bubbles._tcp` in its mDNS section. Run it on Windows (firewall on) and on the Mac.
  Date: · OS: · Result:

### 6.5 Claude with the netdiag window and team chat

- [ ] **Claude Code explains the problems.** Steps: turn on **Agent access (MCP)** in netdiag, connect Claude Code to it over HTTP (the address in Settings), and start a live scan of a local network with at least one problem on the **Problems right now** card. Ask "Use netdiag to tell me what's wrong on the network.": the first line of Claude's answer starts with "OK:", "Problem:" or "Not checked:", and matches the card (Problem with the card's most urgent problem when the card lists one, OK only when the card says "Everything looks fine").
  Date: · OS: · Result:
- [ ] **Claude checks the chat alerts.** Steps: connect Claude Code to netdiag (`netdiag mcp --attach`, or HTTP with **Agent access (MCP)** on) and to bubbles' MCP server with a token of the netdiag app (see docs/mcp.md, "netdiag with team chat (bubbles)"). With one problem fixed and one still open, ask "Use netdiag to check its alerts in team chat." Claude reports the fixed one as fixed and closes it only after you say yes, and leaves the open one open after checking it again. Then cause and fix another problem, and do it again with a token of a chat app of Claude's own: Claude asks you to press **Resolve** in bubbles instead of closing the fixed one. Afterwards have the chat admin remove both tokens in bubbles (**Admin > Apps**).
  Date: · OS: · Result:
- [ ] **Claude Desktop sees the window.** Steps: set `"args": ["mcp", "--attach"]` in Claude Desktop's config, turn on **Agent access (MCP)** in netdiag and start a live scan. Ask "Use netdiag to tell me what's wrong on the network.": Claude sees the live scan. Quit netdiag and start a new chat: Claude says no netdiag window is open, and still answers from the saved scans.
  Date: · OS: · Result:

### 6.6 Defender (v0.6 §7.6)

- [ ] **Release candidate scan.** Steps: run `"%ProgramFiles%\Windows Defender\MpCmdRun.exe" -Scan -ScanType 3 -File <path to the RC netdiag.exe>` (it now uses DPAPI, a start lock and more Bonjour service types). Record the result; a detection is filed as a false positive before release.
  Date: · OS: · Result:
- [ ] **Release build scan.** Steps: the same scan on the release build's `netdiag.exe`, then submit it to Microsoft as a false positive (until code signing lands).
  Date: · OS: · Result:

## Results

| Date | Tester | Build version | Machine / OS | Router model | Printer model | Notes |
|------|--------|---------------|--------------|--------------|---------------|-------|
|      |        |               |              |              |               |       |

# netdiag

netdiag is a single-binary diagnostic tool for local networks. It discovers devices on the local subnet (ICMP, ARP, port probes, SNMP, mDNS, IPP) and reports printers, web services and the health of the network through a local browser UI.

It answers, within about 10 seconds: what is alive on the network, which devices are printers and what state they are in (toner, paper, alerts), which devices offer a web page you can open, whether the gateway, DHCP, DNS and your web server are working, and whether two devices are fighting over the same IP address.

> netdiag can also be used by Claude or other AI agents for troubleshooting. See **Connect Claude** below.

First time? Read **HOW-TO-OPEN.txt** (included in your zip). It explains the SmartScreen (Windows) and Gatekeeper (Mac) steps.

## Quickstart

1. Connect to the network you want to check (Wi-Fi or cable).
2. Unzip the file for your computer and start `netdiag` (double-click `netdiag.exe` on Windows, or `./netdiag` in Terminal on Mac).
3. Your browser opens to the netdiag page on this computer only.
4. Check the interface and address range (pre-filled). Click **Scan**.
5. Read the results:
   - **Devices** (the default view): the **Problems right now** card says what is broken at this moment, or "Everything looks fine" (see **Problems right now** below). Then the stat tiles (Alive, Printers, Web, mDNS, Offline, Conflicts). Click a tile to filter the table to those devices. Below them is the health strip (gateway, DHCP, DNS, internet, Wi-Fi and web server) and the hosts table (see below).
   - **Health** (sidebar, or click a health pill): one card per check, with its status, detail and response time (see **Health panel** below).
   - **Activity**: the live activity log of the scan and of what changed in live mode (see below).
   - **History**: earlier scans, and a **Compare** view that shows what changed between two of them.
   - Click the IP address of a device (or anywhere on its row) to open its **details drawer**: printer model, supplies, SNMP, web links, remote access, conflicts and mDNS, with copy buttons for the IP and MAC.
6. Export with **JSON** or **CSV** (the **Export** menu) if you need to send the results to someone.
7. Click **Quit** when finished (or press Ctrl+C in the Terminal or console window).
8. Want Claude to help you read the results? See **Connect Claude** below.

![netdiag Devices view in the dark theme](docs/img/devices-dark.png)

### The web interface

- **Sidebar navigation**: Devices, Health, Activity, History and Settings. The sidebar becomes a narrow icon rail on medium-width windows and a **menu** button on narrow ones (phones and small windows). Keyboard users can reach every item with Tab, and the current view is marked.
- **Top bar**: the interface, target range and SNMP community fields, the **Live mode** switch, **Scan** / **Stop live** and **Restart watching**, **Export** and **Quit**, then the health summary and icon buttons for muting alerts (bell), enabling desktop alerts (screen) and switching theme (moon/sun). Once team chat is connected, a chat button shows its state (see **Team chat alerts (bubbles)**). Hover an icon to see its name. The **LIVE** indicator pulses while live mode runs.
- **Palette and themes**: the warm dark theme is the default (dark brown with orange accents). Use the sun/moon button in the top bar for the warm light theme; your choice is remembered in this browser only. Both themes meet the WCAG AA contrast levels for text and controls. Status is never shown by colour alone: every status has a shape and a word.
- **Icon legend** (status shapes): up = circle with a check (green); warning = triangle (gold); fail or offline = circle with an X (coral red); unknown or no ping = circle with a question mark; info = "i" (violet). Badges: **NEW** (sparkles), **OFFLINE** (wifi-off), **CONFLICT** (triangle), and a printer icon for printers.
- **Details drawer**: opens on the right with a full description of one device. It stays open while live mode updates the device, and closes with **Esc** or its close button, which also returns the keyboard focus to the row. On narrow screens it fills the window.
- **Activity log**: a terminal-style feed of the scan (start, stages, finish), new, offline and back-online devices, conflicts, health changes, notices and errors, with the time of each entry. Filter it by kind with the chips at the top, and use **Copy** to put the visible lines on the clipboard. It keeps the most recent 500 entries. It is cleared when you reload the page, and is not saved to disk.
- **Motion**: the LIVE indicator, new-device highlights and the tile counts animate briefly. If your computer is set to reduce motion, all animation is switched off.
- **Keyboard**: Tab and Shift+Tab move between controls, Enter opens a row's details, and Esc closes the drawer and the settings dialog. Every control has a visible focus ring.
- **Fonts and offline use**: the Inter and JetBrains Mono fonts are included in the program, so the page looks the same without an internet connection, and makes no outside requests.

![netdiag details drawer in the light theme, showing a conflict: the two devices side by side, what it means and what to try](docs/img/drawer-light.png)

Nothing is installed. No administrator rights are needed. The tool only listens on `127.0.0.1` (this computer), with no login. The web page itself makes no requests to the internet. Apart from name lookups sent to this computer's own DNS servers (the DNS check, and device names during a scan), the only outside request netdiag makes is the optional **internet check** (one quick request to a test address, described under Health panel), which runs from this computer. Once you connect team chat, netdiag also talks to your chat server on the local network.

## Connect Claude (AI agent access via MCP)

netdiag speaks MCP (Model Context Protocol), so Claude or another AI agent on this computer can use it. Claude can read scan results, device details, health checks and history, and it can start scans. It cannot change settings, delete history or ignore MAC addresses.

### Option A: Claude Code (recommended, no window needed)

Open a Terminal (Mac) or Command Prompt / PowerShell (Windows) and run the command for your computer. Use the real path to netdiag.

Windows:

```
claude mcp add netdiag -- "C:\path\to\netdiag.exe" mcp
```

Mac:

```
claude mcp add netdiag -- /path/to/netdiag mcp
```

Check that it is connected:

```
claude mcp list
```

You should see `netdiag` with `✔ Connected`. Then ask Claude something like:

> Use netdiag to scan the network and tell me why the Brother printer is offline.

### Option B: Claude Desktop

Open the Claude Desktop config file. Create it if it does not exist.

- Windows: `%APPDATA%\Claude\claude_desktop_config.json`
- Mac: `~/Library/Application Support/Claude/claude_desktop_config.json`

Paste this block, changing the `command` path to where you unzipped netdiag. On Windows, keep the double backslashes.

```json
{
  "mcpServers": {
    "netdiag": {
      "command": "C:\\path\\to\\netdiag.exe",
      "args": ["mcp"]
    }
  }
}
```

If the file already has other entries, add `"netdiag"` inside the existing `mcpServers` block. Then restart Claude Desktop. netdiag does not need to be open for this option.

To let Claude Desktop see the open netdiag window (the live scan, the problems right now and team chat), use `"args": ["mcp", "--attach"]` and turn on **Agent access (MCP)** in the window's **Settings**. When no window is open, or Agent access is off, it works as with `["mcp"]`, and Claude is told so.

### Option C: share the open netdiag window (HTTP)

With this option, Claude sees the same live scan you see in the netdiag window.

1. Start netdiag. It normally uses `http://127.0.0.1:8787` (see **Live mode**, "Open netdiag again"); to be sure of the port, start it with `netdiag ui --port 8787`.
2. In the netdiag window, open **Settings** and turn on **Agent access (MCP)** (the switch is off by default).
3. Copy the address shown there, which looks like `http://127.0.0.1:8787/mcp`.
4. Add it to Claude Code:

```
claude mcp add --transport http netdiag http://127.0.0.1:8787/mcp
```

This only works while the netdiag window is running. If you already added Option A, remove it first with `claude mcp remove netdiag`.

![Settings with the Agent access (MCP) switch and the endpoint address](docs/img/settings-mcp.png)

### What you can ask Claude

Pick one of netdiag's prompts from Claude's menu, or just ask in your own words: Claude reads netdiag's runbooks and follows the one that fits, so it knows which checks to run. It answers in plain words and starts with **OK**, **Problem**, **Fixed** or **Not checked**. The full list, with what Claude does and which buttons it may ask you to click, is under "Runbooks" in [docs/mcp.md](docs/mcp.md#runbooks).

- **What's wrong right now:** "Use netdiag to tell me what's wrong on the network." (`explain_problems`)
- **The internet is down:** "The internet isn't working. Use netdiag to find out why." (`internet_not_working`)
- **A device stopped answering:** "The front desk PC stopped answering. Use netdiag to check it." (`device_offline`)
- **A new network:** "Use netdiag to give me a first look at this network." (`first_look`)
- **Triage the network:** "Use netdiag to check the network and tell me what needs attention first." (`triage_network`)
- **Printer problems:** "Use netdiag to diagnose the printer at 192.168.1.40." (`diagnose_printer`)
- **IP conflicts:** "Use netdiag to explain the conflict on 192.168.1.23." (`explain_conflict`)
- **A missing device:** "The FRONTDESK laptop is not showing up. Use netdiag to find out why." (`device_missing`)
- **What changed:** "What changed on the network since the last scan? Use netdiag to compare them." (`what_changed`)
- **Check the chat alerts:** "Use netdiag to check its alerts in team chat." (`check_chat_alerts`) Claude re-checks each open alert. When netdiag says one is fixed and you agree, Claude closes it, or asks you to press **Resolve** in bubbles when its chat token can't. For this, Claude needs its own connection to bubbles (a token from your chat admin, best for a chat app of Claude's own), and netdiag connected with `["mcp", "--attach"]` or Option C so Claude sees the window's team chat. See [netdiag with team chat (bubbles)](docs/mcp.md#netdiag-with-team-chat-bubbles) in docs/mcp.md for the token and what it allows.

### Safety

- Claude can only reach netdiag on this computer (`127.0.0.1`). Nothing else on the network can connect to it.
- The HTTP option is off until you turn on **Agent access (MCP)**. Claude cannot turn it on itself.
- Through netdiag, Claude cannot change settings, delete history, ignore MAC addresses, quit netdiag or replace the scan that is watching the network, and it cannot connect, disconnect or post to team chat. Chat alerts are closed by netdiag itself.
- A token you give Claude for bubbles is a separate key with its own powers: a token of the netdiag app lets Claude post and close alerts as netdiag. Prefer a chat app of Claude's own, and have your chat admin remove the token when you no longer need it (see "netdiag with team chat (bubbles)" in [docs/mcp.md](docs/mcp.md#netdiag-with-team-chat-bubbles)).
- The SNMP community string is never shown to Claude, nor a password written in the Web server URL. (Health results saved by netdiag 0.5.7 or earlier may still contain such a password; they age out of History, or delete them there.)
- A scan that Claude starts sends network traffic, just like a click on **Scan**. Only ask Claude to scan networks you are allowed to scan.

### Troubleshooting

**Claude says it has no netdiag tools.**
Check the path in the command or config file, then restart Claude Desktop or start a new Claude Code session. Run `claude mcp list` to see whether netdiag is connected.

**"connection refused" (HTTP option).**
The netdiag window is not running, or **Agent access (MCP)** is off in Settings. Start `netdiag ui --port 8787` again and check the switch.

**Mac: "cannot be opened" or Gatekeeper blocks netdiag.**
Follow the Gatekeeper steps in **HOW-TO-OPEN.txt** (included in your zip) before the first `claude mcp add`. Claude cannot start netdiag while macOS blocks it.

**Windows: SmartScreen blocks netdiag.**
Choose "More info", then "Run anyway", as described in **HOW-TO-OPEN.txt**. Claude cannot start netdiag until SmartScreen allows it.

For the full list of tools, resources and prompts, and the settings details, see [docs/mcp.md](docs/mcp.md).

## What it checks

- **Ping sweep (ICMP)** of every address in the range, in parallel.
- **ARP table**, which finds devices that block ping (Windows firewall often does this).
- **Port probes** on 80, 443, 8080, 8443 (web), 9100, 631 and 515 (printing), and 22, 3389 and 5900 (remote access: SSH, Remote Desktop and VNC screen sharing; see **Remote access**). For web ports it reads the page title, server name and status. For HTTPS it reads the certificate. Self-signed or expired certificates are normal on device pages and are reported, not treated as errors.
- **SNMP v2c** (falling back to v1) read-only queries using the community string (default `public`): system name and description, printer model, serial, page count, supplies, trays, covers, alerts. netdiag never changes device settings.
- **mDNS (Bonjour)** browse on the chosen network: names such as "Brother HL-L2350DW" and the services a device advertises (printing, web, AirPlay and so on). This also finds devices that block ping. See **Printer discovery** below.
- **IPP** (port 631): for printers that answer, the printer's state and supply levels are read when SNMP is off or incomplete. Read-only.
- **Health checks** on the local network (see **Health panel**).
- The **gateway (router)** and **this computer** are labelled.

Scans are limited to ranges of /22 (1024 addresses) or smaller. A /24 typically finishes in under 10 seconds.

## Problems right now

The **Problems right now** card at the top of the Devices view always says what is broken at this moment, most urgent first, or ✔ **OK** "Everything looks fine (checked 10:42)".

- **What it lists.** The same problems the alerts are about: a printer that went offline, a printer alert, a supply at or below the supply threshold, a conflict, and a health check that fails (or could not run). A device going offline counts only when **Any device goes offline** is on in Settings > Alerts, so a phone that went home for the night does not make the card red; it is still listed under the **Problems** filter. Each kind counts only while its alert type is on in Settings > Alerts, and a conflict that involves an ignored MAC is left out.
- **Each item** has a shape and a word (✖ **Problem** for urgent ones, ▲ **Warning**, "i" **Note**), what it is, which device or check and since when. **What to try** opens what it means and up to three things to try; **Show device** opens the device's details, and **Open Health** the health cards. Notes (a health check that only warns) come last, under "Also worth knowing".
- **Fixed in the last day.** A problem netdiag reported and saw fixed stays on the card for a day, as ✔ **Fixed** "Fixed at 10:42 (was down 12 min)". **Dismiss** hides it in this browser.
- **More than five.** The card shows five items; **Show all** lists the rest.
- **When it is not sure.** It says **Checking** while a scan has not finished, **Not checked** when nothing was scanned yet (press **Scan**) or the health checks did not run (**Open Health**, then **Recheck**), and **Paused** or **Stopped** while netdiag holds its checks because this computer lost the network or moved to another one (see **Live mode**). "Everything looks fine" is only said after a finished check.
- **Not watching.** When live mode is off, the card says "netdiag isn't watching right now. This is from the last check at 10:42." Tick **Live mode** and press **Scan** to keep watching.

The card reads the problems again a moment after something changes, and every minute. The Health panel's "Everything looks fine" line comes from the same place, so the two never disagree. Claude reads the same list through `list_problems` (see **Connect Claude**).

## Health panel

The health panel sits above the hosts table. Each row shows an icon, a text status, the check name, a short detail and, where it applies, the response time. Status is always written out in words, never only by colour. Click **Recheck** to run the checks again now (they finish within about 10 seconds). Saving Settings with a new Web server address, trusted certificate or expected answer runs them again too, so the Web server card always describes the address in Settings. They check the network adapter chosen in the interface list. If that adapter is the one used by the running scan, or by the most recent scan since netdiag was opened, they check the router that scan found. Otherwise they check this computer's main router (the one its internet traffic goes to), but only if it is on the chosen adapter's network. If no router is known for the adapter, the Gateway row says so. The header shows a summary such as "Health: 1 fail, 2 warn". When every check came back fine (none fails or warns, and none could not run), the panel says ✔ **OK** "Everything looks fine (checked 10:42)", as long as nothing in the device list needs a look either: no IP or name conflict, no printer alert, no printer supply at or below the supply threshold, and no printer that went offline (or any device, when **Any device goes offline** is on). Each of these counts only while its alert type is on in Settings > Alerts. When one of them does need a look, the line says "All health checks look fine (checked 10:42)" instead, because it then speaks for the health checks only.

**What this means.** A card whose check is not fine says in plain words what that means ("This computer can't reach your router."), lists up to three things to try, and has buttons where they help: **Check again**, **Open Settings**, **Open router page** (for a router that ignores ping but answers on its web page), or **Open Location settings** (Windows, see Troubleshooting). The raw detail the check found sits under **Technical details**; press Esc to close it again.

| Check | What it means |
|---|---|
| **Gateway reachable** | The router answers a ping. Some routers ignore ping, so netdiag then tries to connect to the router on ports 80 and 443 (where most routers have their settings page); an answer there also counts, and the detail says "it does not reply to ping" (or "ping could not run on this computer", when something on this computer stops netdiag from sending pings). A fail means the router answered neither, so this computer cannot reach it at all. |
| **DHCP lease** | Whether this computer got its address from DHCP (normal) or is using a static address. An address in the 169.254.x.x range (APIPA) means DHCP failed, and the check reports `fail`. |
| **DNS resolving** | Whether names can be looked up. netdiag looks up the host of the internet check URL in Settings (`connectivitycheck.gstatic.com` when that URL is the default or uses an IP address). If that fails, it asks this computer's DNS servers directly whether they answer at all. If one does, the check shows `warn` and names that server: either the network has no internet, which is normal for many local setups, or the DNS server cannot reach the internet. If the server finds the name when asked again, the check says DNS is slow; if it says the name does not exist, check the internet check address in Settings. `fail` means none of this computer's DNS servers answered, or none is set. If this local network has no internet on purpose, turn on **This network has no internet** in Settings > Health checks: on this network the check then shows `info` instead of `warn` for this case, so "Everything looks fine" can be reached. The switch is kept for this network only (netdiag knows it by its router), so on any other network, such as an office whose internet is down, the warning still shows. Settings shows the switch once the health checks have looked at the network. |
| **Web server** | A web page you rely on, such as your own site or an intranet page (set the address in **Settings**, "Web server URL"). netdiag reports the HTTP status, the response time and, if you set one, whether the page contains your "must contain" text. When the server sends you on to another page on the same server (for example from http to https, or to its login page), netdiag follows it, up to 5 times, like a browser does, and shows the address it ended up at. A redirect to a different server is not followed: the check fails with the HTTP status and says where it redirects, because the page you set is not the one answering. For an https address the server's certificate must be trusted by this computer, as in a browser; if it is not (common for a self-signed certificate on your own intranet server), see the troubleshooting entry below. If the address carries a user name and password (`http://user:password@server/`), netdiag sends them to the server but shows the password as `xxxxx` everywhere except Settings. If you have not set a URL the check shows "not configured" (info, not a fault). |
| **Internet reachability** | Information only. It asks a test address (`http://connectivitycheck.gstatic.com/generate_204` by default, changeable in Settings) whether the internet is reachable. It never shows `fail`, because a network without internet access is normal for many local setups. With **This network has no internet** on for this network, its card says the check is only for information. |
| **Wi-Fi** | SSID (network name), signal strength, channel, band and link speed, when the computer is on Wi-Fi. A wired computer shows "not on Wi-Fi" (info). When the details cannot be read, it shows "unavailable". On a Mac, macOS can hide the network name unless the app has Location permission; netdiag then shows "hidden by macOS". |

Statuses: `ok` (fine), `warn` (worth a look), `fail` (broken), `info` (for information, including "not configured" and "not on Wi-Fi"), and `unavailable` (netdiag could not read this check, with the reason in the detail). A health problem never stops a scan.

## Conflicts

netdiag watches the address table and the names of devices during a scan and in live mode. It flags three kinds of problem:

- **MAC flapping**: one IP address keeps switching between two MAC (hardware) addresses. This usually means two devices have the same IP. A single change is normal (a device got a new address from DHCP) and is not flagged; the conflict is only raised when the address switches back and forth more than once within 10 minutes.
- **Same MAC on several IPs**: one hardware address answers for several IP addresses. The gateway and this computer are excluded.
- **Duplicate name**: two devices with different MAC addresses report the same hostname, mDNS name or SNMP system name.

Conflicts show as a red or amber **CONFLICT** label on the affected rows, are listed by the **Conflicts** tile and filter on the Devices view (and under **Problems**), and get a section in the detail panel explaining the kind of conflict. The section heading says ✖ **Conflict** only when netdiag saw the two devices take turns at the address (its MAC history shows at least two changes); otherwise it says ▲ **Possible conflict**. For a flapping address or a duplicate name it shows one card per device (Device A, Device B) with its maker (from the MAC address, even for a device no longer answering), MAC address, the other addresses it uses and when it was seen, then what this means and what to try. Other kinds, such as one MAC on several addresses, always say **Possible conflict**.

**Wi-Fi range extenders and mesh nodes.** Many extenders and mesh nodes answer ARP for every device they relay, so several IP addresses share one MAC address. netdiag reports this as a warning with this hint: *if this is an extender or mesh node, use Ignore this MAC.* Click **Ignore this MAC** in the detail panel to stop reporting that MAC. netdiag asks you to confirm first. The ignored MAC is saved in Settings under **Ignored conflict MACs**. To undo it, open Settings, press **Remove** next to it, then **Save**. While a MAC is ignored, netdiag raises no conflict for it (flapping, multiple IPs or duplicate name); the devices themselves still appear in the list.

## Printer discovery (mDNS and IPP)

- **mDNS** finds devices that announce themselves on the local network. netdiag sends its questions from an ephemeral (random) UDP port and does not take over the standard mDNS port, so it does not disturb macOS or Windows. The device's Bonjour name is shown under its IP address, and the services it advertises appear in the detail panel. A device that only shows up through mDNS is marked "mdns service _ipp._tcp" (or similar) as the reason it is treated as a printer.
- **IPP** asks a printer that has port 631 open for its state (idle, processing, stopped), its state reasons (for example "media empty") and its supply levels. The printer's source tag reads **via IPP**, **via SNMP** or **via SNMP + IPP** (both were used, with SNMP values taking priority).
- A printer that answers neither SNMP nor IPP gets a note in its details: it doesn't report its status, so netdiag can only see whether it answers. It suggests turning on SNMP in the printer's settings page, and, when the printer has a web page on its own address, offers **Open its page**.
- If nothing answers mDNS, the scan shows the note "mdns: no responses". This is information only and the scan carries on. A firewall (Windows Defender Firewall, or a VPN client) can block mDNS, and some guest networks do not forward it.
- netdiag uses the network you chose on the first screen. If you have more than one adapter (for example Wi-Fi and a docking station), pick the one connected to the network you want to check.

## Device types

Each device gets a **Type** (shown in the table and the details drawer, and exported as `deviceType`). The type is a best guess from the signals netdiag collected, and is shown as **Unknown** when there are no identifying signals (netdiag never guesses).

| Type (JSON/CSV) | Label | Typical signals |
|---|---|---|
| `self` | This computer | the machine running netdiag |
| `gateway` | Gateway | the default route IP |
| `printer` | Printer | printer detection: SNMP Printer-MIB, IPP, port 9100, mDNS `_ipp` |
| `tv` | TV | mDNS `_airplay`, `_googlecast`, `_raop`, `_roku`, `_spotify-connect`; vendor Roku, Hisense, TCL, Vizio, LG, Sonos; hostname `tv`, `roku`, `chromecast` |
| `nas` | NAS | mDNS `_smb`, `_afpovertcp`, `_nfs`, `_adisk` with a NAS vendor or model; vendor Synology, QNAP, TerraMaster, ASUSTOR; hostname `nas`, `diskstation` |
| `phone` | Phone | mDNS `_apple-mobdev2`; mDNS `_companion-link` when there is no file-sharing or TV service and the name is not a computer name (Macs, Apple TVs and HomePods announce it too); hostname `iphone`, `ipad`, `android`, `galaxy`, `pixel` |
| `computer` | Computer | file-sharing mDNS without a NAS signal; hostname `macbook`, `imac`, `DESKTOP-`, `laptop`, `-PC`, `thinkpad`; Remote Desktop (3389) or screen sharing (5900) with no other signal |
| `camera` | Camera | mDNS `_rtsp`, `_onvif`; vendor Hikvision, Dahua, Axis, Reolink, Amcrest |
| `network` | Network gear | SNMP sysDescr for a switch, router or access point; Deco, UniFi; vendor Ubiquiti, TP-Link, NETGEAR, Cisco, Aruba, MikroTik, Ruckus, Juniper |
| `iot` | Smart home | mDNS `_hap`, `_homekit`, `_matter`, `_hue`; vendor Espressif, Tuya, Shelly, Philips Lighting/Signify, Nest, Ring, ecobee |
| `server` | Server | open web ports 80, 443, 8080 or 8443 with no other signal |
| `unknown` | Unknown | no identifying signals |

Rules are applied in this order and the first match wins: self, gateway, printer, mDNS, SNMP, vendor, hostname, web ports, remote access ports. The details drawer shows the reason for the type (for example "mdns _airplay"). Older saved scans are classified when they are loaded. Compare reports a `deviceType` change only when both scans have a type.

## Remote access

When a device offers Remote Desktop (port 3389), VNC screen sharing (port 5900) or SSH (port 22), netdiag shows it:

- In the hosts table, the **Web / remote** column has a **Remote desktop** or **Screen sharing (VNC)** link when this computer can open it (see the table below). One click starts the session; a short note under the link says where its window appears. When this computer can't open it, use the details drawer instead.
- In the details drawer, **Remote access** lists each service with its port, what will happen, and buttons:
  - **Remote desktop**: **Open remote desktop** (when this computer can open it, see the table below), **Copy address** and **Download connection file** (an `.rdp` file you can keep or send to someone).
  - **Screen sharing (VNC)**: **Open screen sharing** (when this computer can open it) and **Copy address**.
  - **SSH**: **Copy SSH command**, for your IT helper. netdiag does not open SSH sessions.

What opens the session:

| | Windows | macOS |
|---|---|---|
| Remote desktop | Remote Desktop Connection (built into Windows) | Microsoft's free **Windows App** (formerly Microsoft Remote Desktop) from the App Store. Without it, use **Download connection file**. |
| Screen sharing | Only if a VNC viewer that handles `vnc:` links is installed (many don't). Otherwise copy the address into your VNC viewer. | The built-in **Screen Sharing** app |

netdiag only starts the app with the device's address, and only when you click. You sign in with that computer's own user name and password; netdiag never sees or stores them. The device itself must allow remote connections (for Windows: Settings > System > Remote Desktop, available on Pro and higher editions). Sessions are only opened to addresses on a local network (private, link-local or 100.64.x.x addresses), and the row for this computer offers no session to itself.

Live mode never uses these ports to check whether a device is still there, because remote access servers can record every check as a connection attempt. A device that only answers on them is checked by ping or the address table instead. Compare only reports changes in open ports, and device types based on them, that both scans checked, so comparing with a scan from an older netdiag does not show these ports as newly opened.

## Alerts (live mode)

In live mode netdiag raises an alert when something changes: a printer goes offline, a printer reports an alert, a supply runs low, a new device appears, an IP conflict is found, or a health check keeps failing. Alerts appear as a banner stack at the top of the page (up to three at a time, with "N more" for the rest; close each with its button). Critical alerts are announced at once to screen readers; others are announced politely. Each alert makes a beep, with a different tone for critical and warning alerts. An alert's **What to try** opens what it means and what to try, and **Show device** opens the device's details.

- **Fixed.** When the problem goes away, the alert's banner turns into a green ✔ **Fixed** banner, for example "Fixed: Printer back online at 10:42 (was down 12 min). Printer 192.168.1.50 answers again." The last part says what was fixed, such as which health check or which printer alert. Fixed makes no sound. If you already closed the alert, a small Fixed banner appears instead. A desktop notification for the alert is replaced by a silent "Fixed: ..." one. netdiag only says Fixed for an alert it showed you.
- **When an alert counts as fixed.** A device or printer that went offline must answer two checks in a row. A health check must fail two runs in a row before it raises an alert, and pass two runs in a row before it is fixed, so one bad check never raises an alert (and a scan without live mode never raises a health alert). A run where the check could not be done (for example this computer has no route to the gateway) counts neither way. A conflict must be gone from two checks in a row; saving Settings does not count as a check. A low supply must rise more than 5 points above the supply threshold (or reach 100%), report that some is left, or be gone from two printer readings in a row (replaced by a supply with another name). Each printer alert is fixed as soon as the printer stops reporting it.
- **How serious.** Critical (three beeps): the gateway health check failing, a printer that reports an error, and two devices seen taking turns using one address. Warning: a printer offline, a low supply, other conflicts and the other health checks. Note: a new device, and any device going offline (off by default).

- The **bell** button in the top bar mutes or unmutes the beep (a crossed-out bell means muted). The choice is saved in Settings.
- The **screen** button (**Enable desktop alerts**) in the top bar shows a desktop notification too. Your browser asks for permission when you click the button (it must be a click, so the browser allows the prompt). If you say no, the banner and beep still work and netdiag shows a short note. To change your mind later, use the browser's site settings for the 127.0.0.1 address.
- Alerts only fire when something changes, not on every check. An alert is not repeated while its problem lasts, and not within its **cooldown** (default 300 seconds) unless it was fixed in between. If the page loses its connection to netdiag for a moment and reconnects, or you reload it, old alerts come back as banners without sound, and an alert that was fixed in the meantime shows as Fixed (see **Live mode**).
- **Settings** (header button) has the switches for each alert type, the **supply threshold** (percent, 1 to 100, default 15) and the **cooldown** (30 to 3600 seconds). The supply threshold also decides when a printer supply counts under the **Problems** filter. "Device offline" is off by default because it is noisy; "Printer offline" is on.
- On the command line, `netdiag scan --live` prints each alert as a line (`ALERT ...`, and `FIXED Health check OK again DNS check is OK again. (was down 5m)` when it is fixed; JSON lines with `"ok":true` for Fixed with `--json`) and the health summary to stderr.
- What is still open is always on the **Problems right now** card. To have the alerts in your team chat too, see **Team chat alerts (bubbles)** below.

## Team chat alerts (bubbles)

netdiag can post its alerts to your team chat (bubbles), so everyone in the channel sees when something on the local network breaks. Each alert says in plain words what it means and what to try. When the problem is fixed, netdiag closes the same chat card itself, with "Fixed at 10:42 (was down 12 min)" for a device that stopped answering or a failed health check, or "Fixed at 10:42 (after 12 min)" for anything else, such as low toner or an open cover. Connecting takes a few clicks and no typing, with no administrator rights on this computer; your chat admin approves netdiag in bubbles.

**Connect.** Open **Settings**, go to **Team chat (bubbles)** and click **Connect to team chat**. Then:

1. netdiag looks for your chat server on the local network. Click **Use this** next to it. If it is not listed (a firewall or a separate Wi-Fi network can hide it), click **Type the address** and enter the address your chat admin reads in bubbles under Admin > Overview, or **Paste a connection string** your chat admin made under Admin > Apps.
2. **Is this the right server?** netdiag shows the server's fingerprint, with the first four groups large. Ask your chat admin to open **Admin > Overview** in bubbles and read out the start of the fingerprint. If it matches, click **It matches**. If it doesn't, click **It doesn't**: netdiag stops, because something on the network may be pretending to be the chat server. Tell your chat admin.
3. **Ask your chat admin to approve netdiag.** netdiag shows a code. Your chat admin opens **Admin > Pairing** in bubbles, checks that the code matches, ticks a channel for the alerts (or **Also create a channel for this app**) and clicks **Approve**. The request waits 10 minutes. **Open the approval page (for your chat admin)** opens that page on this computer; if the browser warns about the connection, your chat admin approves on their own computer instead. Until netdiag is connected, every step has **Cancel** (or **Close**), which goes back to Settings.
4. **Channel.** netdiag picks the channel your chat admin ticked, or asks you which one when there are several. If netdiag isn't in any channel yet, ask your chat admin to add it to one (for example #netdiag-alerts), then click **Check again**.
5. Click **Send test alert**. A test card appears in the channel and closes itself at once. **Send alerts to team chat** is turned on for you; click **Finish**, then **Close**.

![Connect to team chat: the fingerprint to compare with your chat admin](docs/img/chat-wizard.png)

![Settings with Team chat (bubbles) connected](docs/img/settings-chat.png)

**What goes to chat.** The alerts that are switched on in Settings > Alerts, at the level you choose under **Send**:

- **Problems and warnings** (the default): urgent problems and warnings.
- **Only urgent problems**: a router that does not answer, a printer that reports an error, and two devices seen taking turns using one address.
- **Everything, including notes**: also new devices, and any device going offline when that alert is on.

How serious each alert is follows the same list as the banners (see **Alerts (live mode)**). A chat card names the device, its address, MAC address and maker, the printer model, when the problem started, and a link to the device's own web page when it has one. netdiag never sends the SNMP community.

**Only while netdiag is watching.** Alerts go to team chat only while netdiag is watching this network: leave netdiag open on this computer with **Live mode** ticked. A scan without live mode sends no new alerts (it can still close a card whose problem it found fixed). netdiag is not a server that watches on its own: when this computer is off or asleep, or netdiag is closed, nothing is sent. When this computer itself loses the network, the "netdiag lost the network" notice stays on this computer and is never sent to chat.

**When the chat server can't be reached**, netdiag keeps the alerts and sends them when it is back, with the time each problem really started. An alert whose problem was fixed in the meantime is not sent at all (nobody needs to act on it), and one still waiting after an hour is dropped, because it is no longer news; the problem stays on the **Problems right now** card while it lasts. Waiting alerts are kept when you quit netdiag, and sent once the next live scan has checked that they still matter. **Try now** in Settings tries again at once.

**The chat button** in the top bar shows the state with its shape and colour: sending (green), connected but sending is off (a dashed outline), "can't reach the server", or stopped (no channel, needs reconnecting, or the server's identity changed). The word appears when you point at the button, and in Settings. Click it to open the Team chat section of Settings, which says the state in words, what is wrong, and offers the button that fixes it: **Try now**, **Choose a channel**, **Change channel** or **Reconnect**. See **Troubleshooting** for each one.

**Disconnect.** **Disconnect** in Settings asks first, then stops sending alerts and forgets the connection on this computer. Your chat admin should also remove netdiag in bubbles under **Admin > Apps**.

**Privacy.** Everyone in the channel sees what netdiag posts: device names and addresses, MAC addresses, makers and printer models. Choose a channel for the people who look after the network. netdiag keeps its chat connection key in `bubbles.json` in its data folder, protected with your Windows sign-in (DPAPI) so another user, or a copy on another computer, can't use it; on a Mac the file can be read only by your user account. The key is never in Settings, exports or anything Claude can read.

## Printer status monitoring

In live mode netdiag keeps watching every printer it found through SNMP, so a jam, an open cover or a paused printer is reported within seconds, and so is the fix.

- **Fast status check.** Every few seconds (default 5; set it in **Settings > Scanning > Printer status check in live mode**, 2 to 300 seconds) netdiag reads just three values from each printer in one small SNMP request: the device status, the printer status and the printer's error flags. When the device status or the error flags change, it reads the printer in full straight away and updates the row, the alerts and the banners. Normal idle/printing changes do not trigger a full read.
- **Full refresh.** Every 60 seconds each online printer is also read in full (supplies, trays, alerts). If the fast check fails (for example the printer restarted), the full refresh picks it up again.
- **Error flags become alerts.** Some printers, including Zebra label printers, report problems only through the standard SNMP error flags and leave their alert list empty. netdiag turns those flags into named alerts. Out of paper or toner, door or cover open, paper jam, offline or paused, service requested, missing tray or supply, and output tray full are **critical**. Paper or toner low, output tray nearly full, input tray empty and maintenance overdue are **warnings**. A printer that only reports "down" or "warning" as its device status gets a general alert. Printers that fill in their own alert list are shown as they report it.
- **What you see.** A printer with a critical alert gets a red ✖ status icon (amber ▲ for a warning), even though it still answers ping. The status column and the details panel name the problem (for example "Up, printer error" and "Door or cover open, Printer offline or paused"), and the printer is listed under the **Problems** filter.
- **One alert per problem.** A printer that stops for a reason, such as an open cover or a paper jam, usually reports "Printer offline or paused" as well. netdiag counts that as part of the reason, so opening a Zebra's cover gives one alert, "Door or cover open". "Printer offline or paused" on its own (someone paused the printer) is an alert of its own.
- **Back to normal.** Each printer alert is fixed on its own: when the printer stops reporting it, that alert's banner turns into **Fixed** ("Printer alert cleared") and names the alert. A Zebra clears "Door or cover open" a moment before "Printer offline or paused", so its banner turns into Fixed once both are gone: the cover is closed and the printer has resumed. If the printer fails again soon after, the new alert is shown straight away, without waiting for the alert cooldown. (The screenshots below were taken before v0.6 and still show the older single "Printer OK again" notice.)
- Only printers that answer SNMP get the fast check. Printers found only through IPP or mDNS are refreshed every 60 seconds.

![Zebra printer with its cover open: red status icon and two critical alert banners](docs/assets/shots/real-zebra-fault.png)

![Details panel for the same printer, listing the alerts and the SNMP status](docs/assets/shots/real-zebra-fault-drawer.png)

![The printer fixed: green status and a Printer OK again notice](docs/assets/shots/real-zebra-recovered.png)

![Activity log with the two alerts and the recovery notice](docs/assets/shots/real-zebra-activity.png)

![Settings with the printer status check interval](docs/img/settings-printer-poll.png)

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

The folder holds `settings.json` (your settings) and a `history` folder with one file per saved scan. netdiag also keeps a few small files there: `instance.json` (the address of the running netdiag and the key its page's buttons use, so a second start can open the same page), `open-problems.json` (the problems that are open, and what was fixed in the last day, so a restart neither repeats an alert nor misses a fix), `network-profile.json` (how netdiag recognises the networks it watched), and, once team chat is connected, `bubbles.json` (the chat connection, with its key protected; see **Team chat alerts (bubbles)**) and `chat-outbox.json` (chat alerts still waiting to be sent). Only `settings.json` keeps the SNMP community. Saved scans, exports and everything else netdiag shows outside Settings show it as `***`, so a file you send to someone never gives it away. Scans saved by netdiag 0.5.7 or earlier kept it in clear; netdiag cleans each of those files (it writes `***` in place of the community and changes nothing else) the first time it reads it (for example when you open History). To change the folder for one run, set the environment variable `NETDIAG_DATA_DIR` to the folder you want before starting netdiag (for example `set NETDIAG_DATA_DIR=D:\netdiag-data` in Command Prompt, or `$env:NETDIAG_DATA_DIR="D:\netdiag-data"` in PowerShell, or `NETDIAG_DATA_DIR=/path ./netdiag` in Terminal).

**How to clear it.** To remove one scan, use **Delete** in the History view. To reset your settings to the defaults, quit netdiag and delete `settings.json`. To clear everything, quit netdiag and delete the whole folder above (or the `NETDIAG_DATA_DIR` folder you chose). netdiag creates the folder again the next time it starts.

**A setting netdiag can't use.** If `settings.json` holds a value netdiag can't use (for example a supply threshold of 0 after editing the file by hand), netdiag uses the default for that one setting and keeps all the others. The next time you open Settings it says which ones were reset; click **Save** to keep the corrected values.

## Live mode

Leave **Live mode** ticked (next to **Scan**; your choice is remembered) to keep watching the network after the first scan. Every 15 seconds netdiag checks the addresses where nothing has been found yet. Devices it already knows are not fully probed again (no port scan, web page or name lookup); they get one quick "are you still there?" check. Printers found through SNMP also get a small status check (see **Printer status monitoring**), so live mode stays light on the network.

- The status line reads `Live · watching N addresses · last check Xs ago · M new · K offline`.
- A newly arrived device gets a **NEW** badge and a highlighted row, is listed under the **New** filter, and screen readers announce it.
- Each new device is fully checked once (ports, web page, SNMP/printer details).
- Export JSON/CSV at any time; it includes everything found so far.
- Click **Stop live** to stop watching. To start again with the settings in the top bar, click **Restart watching**: netdiag asks first, and **Keep watching** (the default) leaves it as it is.
- **Reloading the page brings the live view back.** If you reload the page, or close it and open it again at the same address, it shows the live scan again: the table, the health checks, the live status and the alert banners (an alert fixed in the meantime shows as Fixed), without playing the alert sounds again. The Activity log starts again from that point. If you reload during the first scan, the page picks that scan up again, and live mode starts when it finishes.
- **Nothing stops watching by accident.** While netdiag is watching, **Scan** in another tab cannot stop it: netdiag asks whether to restart watching instead. An AI agent (see [docs/mcp.md](docs/mcp.md)) cannot stop watching that you started from the window, from its first scan on: it gets that scan back, and is told to ask you to click **Stop live** (or **Cancel** during the first scan).
- **Open netdiag again to get the page back.** If you closed the browser tab, double-click netdiag again (or run it again): it opens the page of the netdiag that is already running, at the same address, and the console says "netdiag is already running at http://127.0.0.1:8787. Opened it in your browser." Only one netdiag runs at a time for each data folder. netdiag starts at the address it used last, which is `http://127.0.0.1:8787` unless that port was taken; then it picks a free port and remembers it.
- **Buttons only work in a page netdiag opened.** Each time netdiag starts, it opens the page with a new key that its buttons send along, so other users on this computer, and programs that can't read your netdiag data folder, can't press them. A page without the current key (one left open from before netdiag was started again, or one you opened by typing the address) still shows the results, but its buttons and Settings are off and it plays no alert sounds. It says "**Buttons on this page are turned off for safety.** Double-click netdiag to open a fresh page." Once netdiag has opened a fresh page, the pages already open work again too.
- **If netdiag stops.** When netdiag is closed or stops working, the page says "**netdiag has stopped.** Open netdiag again to keep watching." within about 30 seconds. If netdiag was started again, it says "**netdiag was restarted.**" with a **Reload page** button.
- **When this computer loses the network, netdiag pauses its checks.** If this computer's own Wi-Fi or cable drops, or the router and most devices stop answering at once, netdiag does not report every device offline. After this computer sleeps and wakes, the devices that missed checks while it slept are not counted as offline either. When the network is gone, the status line says "Live · checks paused: this computer lost the network (since 10:42)" (or "the router stopped answering"), one "netdiag lost the network" notice appears, and the checks start again by themselves when the network is back, with a "netdiag is back on the network" notice. Nothing about it goes to team chat. If this computer is now on a different network (it was moved, or joined another Wi-Fi), netdiag stops the alerts for the network it was watching ("Live · alerts stopped: this computer is on a different network now"); press **Restart watching** to watch the network it is on now.
- With the page open in several tabs, every tab follows the scan that is watching (also after **Restart watching** in another tab) and shows the alerts, but only the tab you used last plays the sound and shows desktop alerts.

**Offline detection.** Each known device gets one cheap presence check per cycle: a single ping if it was found by ping, otherwise a connection to its lowest open port, otherwise a look at the ARP table (same IP and MAC address). A device that answered ping but stops can still count as present through its open port. After two missed checks in a row a device is marked **OFFLINE** (dimmed row, "offline for 2m", listed under the **Offline** and **Problems** filters, and announced to screen readers); one missed check never flips it. When it answers again it is shown as back online. If an address suddenly shows a different MAC address (another device got the same IP), it is treated as a new device and fully checked once. Your own computer is never checked. Exports include `online` and `last_seen`.

Apart from printers (see **Printer status monitoring**), live mode does not notice changes on devices it already found, such as a changed web page. Run a fresh scan for that.

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
netdiag ui --no-browser                       # start the UI without opening a browser (run netdiag again to open the page)
netdiag ui --port 8765                        # fixed port (80 and 443 are refused; default: the last one, else 8787, else a free one)
netdiag mcp                                   # AI agent access over stdio (see Connect Claude)
netdiag mcp --store-dir D:\netdiag-data       # same, using another data folder
netdiag mcp --attach                          # same, but use the open netdiag window when its Agent access is on
netdiag version
```

The table has a **TYPE** column after VENDOR. `--json` hosts include `deviceType` and `deviceTypeReason`. CSV exports (the UI export and `--csv`) have a final column, `device_type` (column 25; the first 24 columns are unchanged).

`compare` exits `2` when the two IDs are the same, and `1` when a scan is missing or there is no earlier scan to compare with. Use `--csv -` to print the CSV to stdout (not together with `--json`).

Exit codes: `0` success, `1` runtime error (including a scan that was cancelled or timed out, which still prints partial results), `2` invalid arguments (for example a range bigger than /22).

Ranges can be CIDR (`192.168.1.0/24`), a dash range (`192.168.1.10-192.168.1.50`) or a single address.

## Troubleshooting

**Only the router (gateway) responds, or only a few devices show up.**
Wi-Fi "client isolation" (sometimes called AP isolation or guest mode) stops devices from seeing each other. netdiag shows this warning when only the gateway answers. Connect to the main network (not a guest network), or ask the network owner to turn off client isolation. A wired connection to the switch usually avoids the problem.

**A printer shows an error on its own screen but netdiag shows it green.**
Printer status is only watched in live mode, so make sure **Live mode** is on. The printer must also answer SNMP with the community set in Settings: its details panel shows **Data via SNMP** and an SNMP system section. A printer found only through IPP or mDNS is refreshed every 60 seconds instead of every few seconds.

**The printer is found but has no supply or model details.**
SNMP is probably disabled on the printer, or it uses a community string other than `public`. Enable SNMP in the printer's web page (or its front-panel network menu) and set the read community to your network's value, then type that value in the **Community** box and scan again. The printer is still flagged as a printer because its printing ports (9100, 631, 515) are open.

**A device is missing even though you know it is on.**
Some devices block ping. netdiag also reads the ARP table, so a device that has talked to this computer recently should appear. Ask the device to send traffic (open its web page or print a test page), then scan again. Check that you chose the right network in the interface list. The wrong adapter (VPN, virtual machine, Docker) can scan an empty subnet.

**"target range too large (max /22, 1024 hosts)" or the note "subnet truncated".**
netdiag scans at most 1024 addresses (a /22). A bigger range is refused. The automatic suggestion is limited to the /24 around this computer, and you will see a note saying so. Enter a smaller range, or a larger one within /22, in the address box.

**"server: ... address already in use" when starting.**
The port you asked for with `--port` is taken by another program. Start without `--port`: netdiag then uses the address it used last (normally `http://127.0.0.1:8787`), or picks a free port when that one is taken. Starting netdiag while it is already running is not a problem: it opens the page of the netdiag that is running.

**"another netdiag is using the data folder … but is not answering".**
A netdiag for the same data folder is still starting or stopping, or it stopped answering. Wait a few seconds and start netdiag again. If it keeps happening, close the other netdiag's black window (or its Terminal), then start netdiag again.

**The page says "Buttons on this page are turned off for safety".**
The page was not opened by the netdiag that is running now: it was left open from before netdiag was started again, or the address was typed by hand. It still shows the results, but its buttons and Settings are off and it makes no alert sounds. Double-click netdiag again (or run it again): it opens a fresh page, and the pages already open work again too.

**The page says "netdiag lost the network" or "checks paused".**
This computer's own Wi-Fi or cable dropped, or the router and most devices stopped answering at once, so netdiag paused its checks instead of reporting every device offline (see **Live mode**). Check this computer's network cable or Wi-Fi, and that the router is switched on. The checks start again by themselves when the network is back. If it says "this computer is on a different network now", the computer was moved or joined another Wi-Fi: press **Restart watching** to watch the network it is on now.

**Chat alerts stopped.**
Open **Settings > Team chat (bubbles)** (or click the chat button in the top bar). It says what is wrong:

- **Not connected**: click **Connect to team chat**.
- **Can't reach the server**: the chat server is off, or this computer can't reach it. netdiag keeps the alerts and sends them when the server is back; **Try now** tries again at once. If the chat server moved to a new address on the local network, netdiag finds it again by itself when the server announces its new address.
- **No channel**: netdiag isn't allowed to post in its channel any more, or the channel was archived. Ask your chat admin to add netdiag to the channel again and click **Send test alert**, or click **Change channel**. Or no channel has been chosen yet (your chat admin ticked none when approving): click **Choose a channel**, and if netdiag isn't in any channel, ask your chat admin to add it to one, then click **Check again**.
- **Needs reconnecting**: the chat server no longer accepts netdiag (your chat admin may have removed it), or this computer can't open the saved connection (it was copied from another computer or Windows user). Click **Reconnect**. Your chat admin may then see a second netdiag app, and can remove the old one under Admin > Apps.
- **Connected (sending is off)**: **Send alerts to team chat** is off, so nothing is sent. Tick it and click **Save**.
- **Connected**, but nothing arrives: alerts go only while netdiag is watching this network (**Live mode** ticked, netdiag open on this computer), only for the alert types switched on in Settings > Alerts, and only at the level chosen under **Send**. Check that **Send alerts to team chat** is on, then click **Send test alert**.

**Settings says "the chat server's identity changed".**
The chat server answered with a different identity than the one you checked when you connected, so netdiag stopped sending alerts. This happens when the chat server is reinstalled, or when something on the network is pretending to be it. Ask your chat admin before you press **Reconnect**: during Reconnect, compare the fingerprint with the one they read in bubbles under **Admin > Overview**, and click **It doesn't** if it does not match.

**Windows says "Windows protected your PC" or antivirus quarantines the file.**
netdiag is not code-signed. In SmartScreen choose "More info", then "Run anyway". If antivirus removes it, restore it and add the folder as an exception, or ask IT staff to allow it. For IT staff: a scan connects to ten ports on each device it finds, including the remote access ports 22, 3389 and 5900 (to show **Remote access**), so network security tools may report it as remote-access probing from this computer.

**Windows shows a "Windows Security Alert" about netdiag.**
Windows Defender Firewall can ask about netdiag the first time you scan, because a scan waits for answers from devices on the network, such as Bonjour (mDNS) names. The netdiag page itself listens only on `127.0.0.1`, so nothing else on the network can reach it. On an account without administrator rights, **Allow access** asks for an administrator password: click **Cancel** instead. After Cancel, netdiag still finds devices by ping and the address table, reads printers (SNMP and IPP), checks web pages and runs the health checks, because these are answers to requests netdiag sends. Bonjour names may be missing, and a device that answers only Bonjour may not appear. If someone with an administrator password is there, they can click **Allow access** so Bonjour answers get through too. Some work computers block these answers without asking; netdiag then works the same way as after Cancel.

**Mac says the app cannot be opened.**
Run `xattr -d com.apple.quarantine netdiag` in the folder where you unzipped it, then `./netdiag`. See HOW-TO-OPEN.txt.

**The health panel says "not configured" for the web server.**
That is information, not a fault. Click **Open Settings** on the Web server card (or open **Settings**) and enter the web server address in "Web server URL" (and optionally the text the page must contain). Then press **Recheck**.

**The Web server check says "the certificate is not trusted".**
Your web server uses a certificate this computer does not trust: usually a self-signed one, or one from your organisation's own authority. Browsers show a warning for the same page. If it is your own server, click **Trust this certificate** on the Web server card. netdiag shows the certificate's fingerprint (a unique code for that exact certificate) and asks you to confirm. Only do this for your own server. From then on the check accepts that one certificate for the web server, and still warns if it has expired. If the server later gets a different certificate, the check says "the certificate changed since you trusted it" so you can decide again. netdiag trusts one certificate for the web server. If the server sends you on to another of its addresses (another https port) with a different certificate, the check names that address and says it uses a different certificate that is not trusted; to check that page, set its address as the Web server URL. To stop trusting it, open **Settings** and click **Forget** next to "Trusted certificate", then **Save**. Changing the web server address to another server also forgets it.

**The health panel says "not on Wi-Fi" or "unavailable" for Wi-Fi.**
"Not on Wi-Fi" means this computer is on a cable (or Wi-Fi is off), which is fine. "Unavailable" means the Wi-Fi details could not be read. On recent Windows 11 versions this happens when **Location** is off, because Windows treats Wi-Fi details as location data: open Settings > Privacy & security > Location, turn on **Location services** and **Let desktop apps access your location**, then press **Recheck**. The Wi-Fi card's **Open Location settings** button opens that page for you (it opens only that page, nothing else). It also happens when the WLAN AutoConfig service is stopped; the panel says which. The rest of the scan is not affected.

**The health panel says "self-assigned 169.254.x.x address" (DHCP fail).**
This computer did not get an address from the router. Check the cable or Wi-Fi connection, and restart the network connection or the computer. If it keeps happening, ask whoever looks after the network to check the DHCP server (usually the router).

**"mdns: no responses" in the scan notes.**
No device answered the Bonjour (mDNS) query. Devices may still be found through ping, ARP and ports. Check that you picked the adapter connected to the network you want to check. On Windows, the firewall may be blocking the answers, for example after **Cancel** on the Windows Security Alert (see above); someone with an administrator password can allow netdiag in Windows Security > Firewall & network protection > Allow an app through firewall. On a Mac, check that netdiag is allowed in System Settings > Network > Firewall.

**A "CONFLICT" appears on a device that is fine (for example a Wi-Fi extender).**
Click **Ignore this MAC** in the detail panel and confirm. The MAC is saved in Settings under **Ignored conflict MACs**, where **Remove** (then **Save**) undoes it.

**The page does not load after the window opens.**
Keep the black window open: closing it stops netdiag. If the browser did not open, or you closed the tab, double-click netdiag again (or run it again): it opens the page of the netdiag that is already running, with its buttons working. A page you open by typing the address (it begins with `http://127.0.0.1:`, normally `http://127.0.0.1:8787`) shows the results, but its buttons stay off until netdiag opens a page for you.

## Limits (by design)

- IPv4 only. No IPv6 scanning.
- SNMP v2c and v1 only. No SNMPv3, no writes or configuration changes.
- Only the ten fixed ports above are probed. netdiag is not a port scanner or vulnerability tool.
- The last 20 scans are saved on this computer (see **History and compare**). Export anything you need to send to someone; the saved files are only on this computer.
- Apart from name lookups sent to this computer's own DNS servers (which may be on the internet), the only request netdiag makes to the internet is the optional internet check (a single request, server-side, configurable in Settings). The web page makes no external requests.
- When team chat is connected, netdiag also talks to your chat server on the local network (and nothing else). Chat alerts are sent only while netdiag is open and watching on this computer (**Live mode**); netdiag does not keep watching after you quit it or while this computer sleeps.
- One netdiag runs at a time for each data folder: starting it again opens the page of the one that is running.
- Built and tested for Windows and macOS. Linux is not supported.

## Building from source

Requires Go 1.26.9 or newer (go.mod pins `toolchain go1.26.9`; the Go tool downloads it automatically). From the repository root:

```
bash scripts/build.sh      # writes dist/netdiag-<os>-<arch>.zip for all four targets
pwsh scripts/build.ps1     # same, from PowerShell on Windows
```

Set `VERSION=1.2.0` to stamp a version; otherwise `git describe` is used. Each zip contains the binary, this README and HOW-TO-OPEN.txt.

Licence: netdiag is Apache-2.0 ([LICENSE](LICENSE), [NOTICE](NOTICE)). Both build scripts run `go run ./tools/licenses -check` and refuse to build when THIRD_PARTY_LICENSES.txt is out of date; after changing a Go dependency, regenerate it with `go run ./tools/licenses`.

Dev note: the icons are a vendored subset of [Lucide](https://lucide.dev) (ISC licence, `web/vendor/LICENSE-lucide.txt`), packed into the sprite `web/icons.svg`. The sprite is regenerated only by the dev-only tool in `tools/icons` (`npm ci && node build-sprite.mjs`). You do not need it to build or run netdiag.

Refresh the vendor database with `bash scripts/update-oui.sh` (needs network access).

## Roadmap

Planned work, from the next patch to the v1.0.0 Microsoft Store release, is in [roadmap/README.md](roadmap/README.md), with a detailed plan per version.

## License

netdiag is licensed under the Apache License, Version 2.0: see [LICENSE](LICENSE) and [NOTICE](NOTICE). The third-party components built into netdiag and their licenses are listed in [THIRD_PARTY_LICENSES.txt](THIRD_PARTY_LICENSES.txt), which is also in every release zip.

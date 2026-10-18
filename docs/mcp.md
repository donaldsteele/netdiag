# netdiag MCP reference

netdiag can be used by Claude or other AI agents through the **Model Context Protocol (MCP)**. An agent connected to netdiag can read scan results, device details, health checks, the problems netdiag sees right now and history, and can start scans. It cannot change settings, delete history, ignore MAC addresses, quit netdiag, restart or replace the scan that is watching the network, connect or disconnect team chat, send alerts to chat, choose the chat channel or open Windows settings pages.

For a plain-language setup guide, see **Connect Claude** in the main [README](../README.md#connect-claude-ai-agent-access-via-mcp).

## Overview

netdiag offers MCP in two ways:

| Method | How it runs | Needs the netdiag window open? | Live mode | Turned on by |
|---|---|---|---|---|
| **stdio** (`netdiag mcp`) | The agent starts its own netdiag process | No | Not available | Always available |
| **stdio, attached** (`netdiag mcp --attach`) | The agent starts netdiag, which passes everything on to the open netdiag window | No: without a window (or with Agent access off) it works like `netdiag mcp` | Available while attached | **Settings > Agent access (MCP)** for the window |
| **HTTP** (`/mcp` on the UI server) | Shares the running netdiag window | Yes | Available | **Settings > Agent access (MCP)** (off by default) |

All methods use the same tools, resources and prompts, and the same safety rules. Use stdio when you want Claude to run a scan on its own. Use HTTP, or `--attach` for clients that only start programs (Claude Desktop), when you want Claude to see the same scan you see in the browser.

Scans sent by an agent send real network traffic, just like the **Scan** button. Only connect agents to a network you are allowed to scan.

## Connection methods

### stdio: `netdiag mcp`

```
netdiag mcp [--store-dir DIR]
```

- Speaks MCP over standard input and output. Logs go to stderr only, so stdout carries only protocol messages.
- Settings and history are read from the same folder as the UI (see the README, "Where the data is kept"). Use `--store-dir DIR` to choose another folder for this run. If the folder cannot be opened, netdiag prints a warning to stderr and continues.
- Ctrl+C or the agent closing the connection stops the process. Scans it started are cancelled and their partial results are kept.

### stdio attached to the window: `netdiag mcp --attach`

```
netdiag mcp --attach [--store-dir DIR]
```

- When the netdiag window for the same data folder is open and its **Agent access (MCP)** is on, `--attach` passes the agent's messages on to the window's own MCP address and back. The agent then sees what the window sees: the scan watching the network (`get_status` `watching`), the problems right now and team chat. `get_status` answers `"transport": "http"` and `window.thisProcess: true`.
- netdiag finds the window through `instance.json` in the data folder and checks that the netdiag answering on that port is really the one that wrote it (another user's netdiag on the same port does not count).
- With no window open, or with Agent access off, it runs on its own, exactly like `netdiag mcp`. It prints one line to stderr saying why, and `get_status` says it in its `note`: "No netdiag window is open" or "The netdiag window is open but Agent access is off".
- It never gets around Agent access: turning it off in the window closes the attached session too. Then, or when the window quits, `netdiag mcp --attach` answers the next request with an error and exits (the client shows the server as stopped; start a new session to attach again or run on its own). A single request the window turns down while it is still there gets the window's own error, as over HTTP, and the session goes on.
- Choose it when it starts: a window opened later is not picked up until the agent starts `netdiag mcp --attach` again.

### HTTP: the running netdiag window

1. Start the UI on a fixed port: `netdiag ui --port 8787`. It opens the netdiag page in your browser. (If netdiag is already running, this opens its page instead.)
2. In that page, open **Settings**, find **Agent access (MCP)**, and turn on **Allow AI agents to connect (MCP)**. Click **Save**. Settings only work in a page netdiag opened: if the page says "Buttons on this page are turned off for safety", run netdiag again to open a fresh one.
3. The console prints `netdiag MCP: http://127.0.0.1:8787/mcp (enabled)`. The same URL is shown in Settings with a **Copy** button.

The endpoint is `http://127.0.0.1:<port>/mcp` and accepts `POST`, `GET` and `DELETE`. While Agent access is off, every request to `/mcp` returns `404` with a JSON message: `MCP is disabled; enable it in Settings > Agent access`. Turning it off in Settings closes any open agent sessions.

Because the endpoint is only on `127.0.0.1`, agents must run on the same computer as netdiag.

## Client configuration

### Claude Code (recommended)

stdio, Windows:

```
claude mcp add netdiag -- "C:\path\to\netdiag.exe" mcp
```

stdio, Mac:

```
claude mcp add netdiag -- /path/to/netdiag mcp
```

HTTP (netdiag window open, Agent access on):

```
claude mcp add --transport http netdiag http://127.0.0.1:8787/mcp
```

Check the connection:

```
claude mcp list
```

A working entry shows `netdiag: ... - ✔ Connected`. Remove an entry with `claude mcp remove netdiag`. Claude Code adds the entry to its local config by default (`--scope local`).

### Claude Desktop

Open the config file in a text editor (create it if it does not exist):

- Windows: `%APPDATA%\Claude\claude_desktop_config.json`
- Mac: `~/Library/Application Support/Claude/claude_desktop_config.json`

Add the `netdiag` entry (on Windows, use the real path and double the backslashes):

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

On Mac, use the path to the `netdiag` file, for example `"command": "/Applications/netdiag/netdiag"`, with `"args": ["mcp"]`. If the file already has other `mcpServers` entries, add `netdiag` inside the same object instead of creating a second one. Restart Claude Desktop after saving.

Claude Desktop uses stdio only. To let it see the open netdiag window (the scan watching the network, the problems right now and team chat), use `"args": ["mcp", "--attach"]` and turn on **Agent access (MCP)** in the window. Without a window it works as with `["mcp"]`.

## Runbooks

Runbooks are netdiag's step-by-step guides for Claude: which tools to call, in what order, and how to answer someone who is not technical. Each one is an MCP prompt, and the same text is available through the `get_runbook` tool and the `netdiag://runbooks` resources. Every answer starts with a status word: **OK**, **Problem**, **Fixed** or **Not checked**.

| Situation | Say this to Claude | Runbook | What Claude does | Buttons Claude may ask you to click | Needs the netdiag window? | Since |
|---|---|---|---|---|---|---|
| Something is wrong, but what? | "Use netdiag to tell me what's wrong on the network." | `explain_problems` | Reads the **Problems right now** list and explains each problem, most urgent first, with netdiag's own advice, then what was fixed in the last day. | **Live mode**, **Scan**, **Restart watching**, **Open Health**, **Recheck**, **Open router page**, **Check again**, **Open Settings**, **Show device** | No, but with it Claude sees the live scan | 0.6.0 |
| The internet is down | "The internet isn't working. Use netdiag to find out why." | `internet_not_working` | Runs the health checks and finds where the connection breaks: this computer, the Wi-Fi or cable, the router, name lookup or the internet line. Then whether it is just this computer. | **Open Location settings**, **This network has no internet**, **Settings**, **Recheck**, **Health** | No | 0.6.0 |
| A device stopped answering | "The front desk PC stopped answering. Use netdiag to check it." | `device_offline` | Checks that one device, reads when it was last seen and how it was found, and whether it moved to another address. | none | No | 0.6.0 |
| Are the chat alerts right? | "Use netdiag to check its alerts in team chat." | `check_chat_alerts` | Matches netdiag's open alerts in team chat with what netdiag sees now. A fixed one is closed only when netdiag confirms it and you say yes. | **Try now**, **Settings > Team chat (bubbles)**, **Reconnect**, **Admin > Apps**, **Change channel**, **Choose a channel**, **Connect to team chat**, **Send alerts to team chat**, **Live mode**, **Scan**, **Resolve**, **Acknowledge** | Yes: `--attach` or HTTP, and Claude connected to bubbles too | 0.6.0 |
| A network you don't know yet | "Use netdiag to give me a first look at this network." | `first_look` | Describes this computer, the router and what is on the network (counted by type), then what to fix first. Asks before scanning. | **Scan**, **Live mode**, **Settings > Team chat (bubbles)**, **Connect to team chat** | No | 0.6.0 |
| What needs attention first? | "Use netdiag to check the network and tell me what needs attention first." | `triage_network` | Uses the scan that is watching the network (or asks, then scans), checks health, goes through the problems with their advice, and suggests likely causes. | none | No | 0.5.0 |
| A printer is not working | "Use netdiag to diagnose the printer at 192.168.1.40." | `diagnose_printer` | Reads the printer's details, supplies and alerts, checks it again, and says what to do. | none | No | 0.5.0 |
| Two devices may share an address | "Use netdiag to explain the conflict on 192.168.1.23." | `explain_conflict` | Explains the conflict and the two devices side by side, with netdiag's own verdict: "Conflict" or "Possible conflict". | **Ignore this MAC** | No | 0.5.0 |
| A device is missing from the list | "The FRONTDESK laptop is not showing up. Use netdiag to find out why." | `device_missing` | Searches the current scan and history, and reads the offline timeline. | none | No | 0.5.0 |
| What changed? | "What changed on the network since the last scan? Use netdiag to compare them." | `what_changed` | Compares two scans and describes what was added, removed and changed. | none | No | 0.5.0 |

The buttons are the ones a runbook may name; Claude can't press any of them. **Resolve** and **Acknowledge** are buttons in bubbles, and **Admin > Apps** is a page there.

### How to start a runbook

- **Claude Code:** type `/` and pick the runbook from the menu (netdiag's prompts are listed as `/mcp__netdiag__explain_problems` and so on). Claude asks for any argument it needs.
- **Claude Desktop:** the runbooks show up as prompts in the attachments menu. This has not been tested yet.
- **In your own words**, in any client: just ask, as in the table. Claude reads the list with `get_runbook` (or `netdiag://runbooks`) and follows the one that fits.

### What each runbook does

Calling a prompt without a required argument returns an error.

| Prompt | Arguments | What the agent does |
|---|---|---|
| `explain_problems` | none | Explain every open problem in plain words, most urgent first, with netdiag's own advice and buttons, and what was fixed in the last day. The answer starts with **OK**, **Problem** or **Not checked**. Not checked says why, in the window's words, with its buttons: no scan yet (**Scan**), a scan still going (wait), or health checks that did not run (**Open Health**, then **Recheck**). While netdiag has paused its checks, the answer says so first. |
| `internet_not_working` | none | Run the health checks and find where the connection breaks: this computer, the Wi-Fi or cable, the router, name lookup or the internet line. Then whether it is just this computer. |
| `device_offline` | `device` (optional; the agent asks) | Why one device stopped answering: probe it, read when it was last seen and how it was found, and whether it moved to another address. |
| `check_chat_alerts` | `channel` (optional; default: the channel netdiag posts to) | Go through netdiag's open alerts in team chat with the bubbles tools (`list_alerts`, `resolve_alert`), matching each one to `list_problems` by its key (also after a chat admin renamed netdiag's app). Still a problem: re-check it once and say since when, and leave it open. Fixed: netdiag closes it itself; Claude closes it only when netdiag confirms it is fixed, it is still open a minute later, and you say yes, and it asks netdiag once more right before closing it. The answer starts with **OK**, **Problem** or **Not checked**: Not checked when Claude could not read the alerts (no connection to bubbles, or a connection that does not see the window's team chat) or netdiag does not list one of them. A team chat problem is explained in the window's words, with its button; when the chat server's identity changed, Claude tells you to ask your chat admin before you press **Reconnect**. It is told never to post new alerts or acknowledge them, and it adds a note to an open alert only after your yes (that needs `messages:write`). Needs Claude connected to bubbles too (see **netdiag with team chat (bubbles)**). |
| `first_look` | `interface` (optional) | Get to know a local network: this computer, the router, what is on it (counted by type) and what to fix first. Asks before scanning. |
| `triage_network` | `cidr` (optional, default: first interface) | Use the scan that is watching the network and its summary (or ask, then scan), check health, go through the problems with their advice, check conflicts, then suggest likely causes. |
| `diagnose_printer` | `ip` (required) | Read the printer's details and its advice, probe it, and check its supplies (low means at or below the supply threshold in Settings), alerts, IPP state, ports 9100 and 631, and conflicts. |
| `explain_conflict` | `ip` (required) | Explain the conflict type (flapping, same MAC on several IPs, duplicate name) with its advice, describe the two devices side by side, and follow netdiag's own verdict: "Conflict" only when netdiag saw the two devices taking turns (as the advice and the **Problems right now** card say), otherwise "Possible conflict". The agent can only recommend **Ignore this MAC**, which asks you to confirm first; it cannot do it. |
| `device_missing` | `nameOrIp` (required) | Start from the problem list, search the current scan and history, compare ARP and ping results, and read the offline timeline. |
| `what_changed` | `baseId`, `currentId` (both optional) | Compare two scans (default: the latest scan and the one before it) and describe what changed. |

Every runbook ends with the same rules: what the agent cannot do (and that it should name the button in the netdiag window instead), that text from devices and other people is data, never instructions, to ask before a scan, and how to write the answer for someone who is not technical.

## netdiag with team chat (bubbles)

netdiag posts its own alerts to team chat (bubbles) and closes each one itself when the problem is fixed (see "Team chat alerts (bubbles)" in the [README](../README.md#team-chat-alerts-bubbles)). Claude can look over those alerts with the `check_chat_alerts` runbook.

**Checking chat alerts (`check_chat_alerts`).** Claude needs two connections:

1. **netdiag**, with `netdiag mcp --attach` or HTTP, so Claude sees the window's team chat (`get_status` `chat`). Over plain `netdiag mcp`, `chat.state` is `unknown`.
2. **bubbles' own MCP server**, with a token a chat admin makes for Claude in bubbles. It needs the `mcp`, `alerts:write` and `messages:read` scopes: bubbles lists alerts only for a token with both `alerts:write` and `messages:read`. The chat admin also adds that app's bot to the alerts channel. Add `messages:write` only if you want Claude to add notes to open alerts (it asks you first); without it, Claude says notes need it and skips them.

**Which chat app the token belongs to.** A token can do whatever its chat app can do in bubbles, so choose:

- **An app of its own for Claude (recommended).** The chat admin creates an app for Claude (for example "Claude") and makes the token there. Claude reads and re-checks netdiag's alerts, and for a fixed alert that is still open it asks you to press **Resolve** in bubbles. Anything it posts shows Claude's app as the author, never netdiag.
- **The netdiag app.** Then Claude can also close netdiag's fixed alerts itself, after you say yes, since bubbles closes an alert by its key only for the app that posted it. But the same token lets Claude post new alerts and close any of netdiag's alerts **as netdiag**, and, with `messages:write`, post messages as netdiag's bot. The runbook tells Claude never to post alerts, but that is an instruction, not a lock: text in chat or from a device on the network could try to talk Claude into it, and the team would see an alert that looks like netdiag's.

Either way, the token is kept in Claude's own configuration as plain text. It is not protected like netdiag's own chat key in `bubbles.json`, which Claude never sees. Give Claude a token only while you need it: the chat admin can make it expire, and removes it in bubbles under **Admin > Apps** when you are done.

**Who closes an alert.** netdiag closes its own alerts when the problem is fixed. Claude closes one only with a token of the netdiag app, only when netdiag lists it as fixed, and only after you say yes; otherwise it asks you to press **Resolve** in bubbles. The runbook tells Claude never to post new alerts or acknowledge them.

netdiag's tool and prompt names never clash with bubbles' (v0.2.0) tools, so Claude can use both servers at once.

## Tools

All tools return compact JSON. List tools return pages (use `cursor` and `nextCursor`). Invalid input or an unknown scan returns an error message that says what to fix.

Kind: **Read** tools only read what netdiag already knows. **Probe** tools send network traffic, like the **Scan** button: one scan, one health check run, or one device. **Stop** ends a scan this netdiag process started.

| Tool | What it does | Key inputs | Kind | Since |
|---|---|---|---|---|
| `list_interfaces` | Lists usable network adapters with the suggested scan range for each. | none | Read | 0.5.0 |
| `start_scan` | Starts a scan and waits up to `waitSec` (default 30) for it to finish. If a scan is already running, returns that scan with `alreadyRunning: true`. It never replaces a scan that is watching the network: that scan keeps running and comes back with `alreadyWatching: true` too. | `cidr` (max /22, default: suggested), `interface`, `community`, `waitSec` (0–120), `live` (HTTP only), `liveIntervalSec` | Probe | 0.5.0 |
| `get_scan` | Summary of a scan: counts, health, conflicts and printer alerts. Checks the running scans first, then saved history. | `scanId` | Read | 0.5.0 |
| `cancel_scan` | Cancels a scan started in this netdiag process. Results collected so far are kept. A live-mode scan started from the netdiag window keeps running, including during its first scan: the answer is `cancelled: false` with a note to ask the person to click **Stop live** (or **Cancel** while its first scan runs). | `scanId` | Stop | 0.5.0 |
| `list_devices` | Paged device rows for a scan. | `scanId`, `filter` (all, printers, web, offline, conflicts, new, mdns, problems), `deviceType`, `query` (IP, MAC, name, vendor or mDNS service text such as `_ipp._tcp`), `limit` (default 50, max 200), `cursor` | Read | 0.5.0 |
| `get_device` | Full detail for one device: ports (including the remote access ports 22, 3389 and 5900), web services, SNMP, printer supplies and alerts, and conflicts involving it. Agents cannot start remote sessions; that is a button in the netdiag window. | `scanId`, and `ip` or `mac` | Read | 0.5.0 |
| `get_events` | Change feed of a scan started in this process: new, offline and back-online devices, printer changes, conflicts, health changes, alert notices, and `fixed` entries (a problem netdiag reported is fixed; `data.key` is the key of the notice it closes). | `scanId`, `sinceSeq` (pass the last `nextSeq`), `limit` (default 100, max 500) | Read | 0.5.0 |
| `get_health` | Latest health report (gateway, DHCP, DNS, internet, Wi-Fi, web server). Says `none` if there is none yet. | none | Read | 0.5.0 |
| `run_health_check` | Runs the health checks now (about 10 seconds) on the network adapter of the running scan, or of the most recent scan since netdiag was started, with the router that scan found. With no such scan it checks the adapter that carries the default route and that route's router. A router is checked only if it is on the adapter's network; otherwise the Gateway check says no router is known for this network adapter. | none | Probe | 0.5.0 |
| `probe_host` | Checks one device on a local subnet. Nothing is saved. | `ip` (must be on a local interface subnet), `community` | Probe | 0.5.0 |
| `list_history` | Saved scans, newest first. | `limit` (default 20, max 100) | Read | 0.5.0 |
| `compare_scans` | What was added, removed and changed between two scans (up to 200 rows per list). | `baseId` (default: previous scan of the same range), `currentId` | Read | 0.5.0 |
| `get_settings` | Current settings. The SNMP community is shown as `***`, and a password in the web server URL (`http://user:password@host/`) as `xxxxx`. | none | Read | 0.5.0 |
| `get_status` | Where to start. Whether the netdiag window is open and serves this connection (`window.thisProcess`), whether Agent access is on, the scan watching the network (`watching`, with its `scanId`, or `null`), the scan running now, a count of the problems with `checkedAt`, and team chat (`chat`: `state`, `server`, `channel`, `appName` (the chat app netdiag posts as), `sendAlerts` and `minSeverity` (the **Send alerts to team chat** switch and level), `waiting` (what still waits to be sent: alerts, the notes that close them, and messages; alerts older than an hour are dropped), `lastSentAt`, and `since` while the chat server does not answer). `chat.state` is `off`, `connected`, `no_channel`, `unreachable`, `needs_reconnect`, `identity_changed`, or `unknown` when this connection is not the window's. `note` says in a sentence what this connection can see: over stdio, `watching` is always `null`, and the note says whether a window is open (with Agent access on or off). It never shows a token. | none | Read | 0.6.0 |
| `list_problems` | What is wrong right now, most urgent first: the same list as the **Problems right now** card. Each open problem has `key` (the same as its team chat alert), `kind`, `severity` (`critical`, `warning` or `info`), `title`, `device`, `since`, `advice` (`meaning`, `steps` and the `actions` buttons, in netdiag's own words), `chat.posted`, and `recheck` (the tool and arguments that check it again). `everythingFine` is `true` only when a finished check that is not paused, with health results (unless health alerts are off), found nothing that is a warning or worse. It is also `false` when nothing was checked yet (`source` `none`), when only the health checks ran and no device was looked at yet (`source` `health`), while a scan is still on its first pass, while the checks are paused (`held`, with `heldReason`), and when the health checks did not run: `false` with no warning means "not checked", not a problem. `fixed` lists what was fixed in the last 24 hours (`detail` says which check or supply, `fixedAt`, `downForSec`). `source` is `live`, `latest`, `saved`, `health` or `none`; over stdio it is the newest saved scan. | `includeFixed` (default true), `limit` (default 50, max 200) | Read | 0.6.0 |
| `get_runbook` | netdiag's runbooks (see **Runbooks** above) as a tool, for clients without prompts. Without a name, returns `{"runbooks": [...]}`, each with `name`, `title`, `description`, `arguments` and `since`. With a name, returns `{"name", "title", "text"}`, where `text` is the same as the prompt (and as `netdiag://runbooks/{name}`). A missing required argument is an error, as for the prompt. | `name`, `arguments` | Read | 0.6.0 |

**What `filter=problems` lists.** The same devices as the **Problems** filter in the netdiag window:

- a device that live mode marked offline (this computer never counts as offline),
- a device involved in a conflict (except a conflict that involves a MAC ignored with **Ignore this MAC**),
- a printer with an alert, or with a supply that is low, empty, or at or below the **supply threshold** in Settings (`alerts.supplyThresholdPct`, default 15%),
- a device whose web page did not answer or answered with an HTTP error (status 400 or higher).

`filter=offline` and `filter=conflicts` use the same offline and conflict rules. The printer alerts in the `get_scan` and `start_scan` summaries are the printers this filter lists for an alert or a supply, with the same threshold; `lowSupplies` names each supply at or below it.

`get_events` and `live` work only for scans started in the same netdiag process. Scans saved earlier are available through `get_scan`, `list_devices` and `compare_scans`.

## Resources

Resources return JSON (`application/json`). Read them with the client's resource browser or let the agent read them.

| URI | Contents |
|---|---|
| `netdiag://interfaces` | Same as `list_interfaces` |
| `netdiag://health` | Same as `get_health` |
| `netdiag://history` | Same as `list_history` with default limit |
| `netdiag://settings` | Same as `get_settings` (community and web server URL password redacted) |
| `netdiag://status` | Same as `get_status` |
| `netdiag://problems` | Same as `list_problems` with defaults |
| `netdiag://runbooks` | Same as `get_runbook` without a name: every runbook with its arguments |
| `netdiag://scans/{scanId}` | Scan summary |
| `netdiag://scans/{scanId}/devices` | First 200 device rows of the scan |
| `netdiag://scans/{scanId}/devices/{ip}` | Full detail for one device |
| `netdiag://runbooks/{name}` | One runbook's text, as the prompt gives it. A required argument shows as a placeholder, for example `<ip>` |

`resources/list` shows only the seven fixed resources. Scans and runbooks are not listed one by one: `resources/templates/list` shows the three `netdiag://scans/...` templates and `netdiag://runbooks/{name}`. The scan ids to fill in come from `netdiag://history` (or the `list_history` tool) and from `start_scan`, the runbook names from `netdiag://runbooks`. A scan template works for saved scans and for scans running in this netdiag process.

## What Claude can never do

These stay buttons in the netdiag window. None of netdiag's MCP tools does them, the code that would is kept out of the MCP package, and every runbook ends by telling Claude to name the button instead. Through netdiag, Claude cannot:

- change settings
- delete saved scans
- ignore a device in conflict checks
- quit netdiag
- restart or replace watching
- connect or disconnect team chat
- send test alerts or post netdiag alerts to chat
- choose the chat channel
- open Windows settings pages

These are the limits of netdiag's own tools. A token you give Claude for bubbles' MCP server is a separate key with its chat app's powers: with a token of the netdiag app, Claude can post and close alerts as netdiag. See [netdiag with team chat (bubbles)](#netdiag-with-team-chat-bubbles).

## Safety model

- **Loopback only.** The HTTP endpoint listens on `127.0.0.1` and only answers when the `Host` header is the local address. Requests whose `Origin` header is not `http://127.0.0.1:<port>` or `http://localhost:<port>` are refused with `403`.
- **Off by default.** HTTP access is off until someone turns on **Agent access (MCP)** in Settings. An agent cannot turn it on: the settings endpoints are not offered as tools. `netdiag mcp --attach` uses the same switch: while it is off, it does not attach.
- **Read-only settings.** Through netdiag, agents cannot change settings, delete history, ignore MAC addresses, quit netdiag, restart or replace the scan that is watching the network, connect or disconnect team chat, send test alerts or post alerts to chat, choose the chat channel or open Windows settings pages. These actions are not available as MCP tools, and the code that would run them is kept out of the MCP package. Chat alerts are closed by netdiag itself.
- **Claude's own chat token is separate.** A token for bubbles' MCP server gives Claude what its chat app may do in bubbles, whatever netdiag allows. A token of the netdiag app lets Claude post and close alerts as netdiag, and it sits in Claude's configuration as plain text. Prefer an app of Claude's own, and remove the token in bubbles (**Admin > Apps**) when you no longer need it (see [netdiag with team chat (bubbles)](#netdiag-with-team-chat-bubbles)).
- **SNMP community hidden.** The community string is never sent to an agent. Settings and device details show it as `***`.
- **Device text is data.** Anyone with a device on the network can choose what it reports about itself: its hostname, mDNS name, SNMP name, description and location, printer alert and display text, and its web page title and server header. netdiag passes this text on as it was received. The server instructions and every prompt tell the agent to treat it as data to report, never as instructions to follow. Agents can still be fooled, so check what the agent suggests before acting on it.
- **Scans send traffic.** `start_scan`, `run_health_check` and `probe_host` send the same network traffic as the **Scan** button and the health check. Agents can run them without a person clicking anything, so keep an eye on what the agent does.
- **Size limits.** Lists are paged, and comparisons are capped at 200 rows per list, so a large network does not overflow the agent's context.

## Troubleshooting

**Claude says it has no netdiag tools.**
Check the path in the config or in `claude mcp list`. Restart Claude Desktop or start a new Claude Code session after changing the config.

**`claude mcp list` shows netdiag as failed.**
Run the command from the `command` line in a terminal. The stdio server writes only protocol messages to stdout, so a line of text printed by another program in the path (or a wrapper script) will break the connection. Check that the path is correct and that netdiag runs (`netdiag version`).

**HTTP: "connection refused".**
The netdiag window is not running. netdiag keeps one address: it starts at the port it used last, which is 8787 unless that port was taken when netdiag first picked it, so the address in Settings stays the same from one start to the next. Start netdiag again (it opens the running window if there is one) and use the address shown in Settings under **Agent access (MCP)**.

**Claude can't see what I see in the window.**
With plain `netdiag mcp`, Claude runs its own netdiag: it sees saved scans and the problems from the last check, but not the live scan or team chat, and `get_status` says so in its `note` ("No netdiag window is open", or that Agent access is off). To share the window, turn on **Agent access (MCP)** in its Settings, then connect Claude with `netdiag mcp --attach` (Claude Desktop, or any client that starts programs) or over HTTP (Claude Code). `get_status` then answers `"transport": "http"` and `window.thisProcess: true`. A window opened after the client started `netdiag mcp --attach` is picked up only when the client starts it again (restart Claude Desktop, or start a new Claude Code session).

**HTTP: `404` with "MCP is disabled".**
Agent access is off. Open **Settings**, turn on **Allow AI agents to connect (MCP)**, and click **Save**. If Settings says it is turned off on this page for safety, run netdiag again: it opens a fresh page where Settings work.

**HTTP: `403`.**
The request came from a different address or Origin. Use `http://127.0.0.1:<port>/mcp` (not a local network address or a host name).

**Windows Firewall asks about netdiag.**
The alert comes from scanning, not from MCP: a scan waits for answers from devices, such as Bonjour (mDNS) names. The HTTP method listens only on `127.0.0.1`, so it is reachable from this computer only. The stdio method opens no network port. Without an administrator password, click **Cancel**; scans still work, but Bonjour names may be missing. See "Windows shows a Windows Security Alert about netdiag" in the main README troubleshooting section.

**A scan started by an agent finds the wrong network.**
Call `list_interfaces` to see the adapters and their suggested ranges, then pass `interface` or `cidr` to `start_scan` explicitly. A VPN or virtual adapter can be picked by default.

## Tested versions

| Client | Version | Transport | Platform | Result |
|---|---|---|---|---|
| Claude Code | 2.1.296 | stdio (`claude mcp add`, then `claude mcp list`) | Windows 11 | Connected |
| Claude Code | 2.1.296 | HTTP (`--transport http` to `http://127.0.0.1:8787/mcp` with Agent access on) | Windows 11 | Connected |
| Scripted MCP client over stdio | — | stdio attached (`netdiag mcp --attach`, window on a free port with Agent access on) | Windows 11 | Attached: `get_status` gave `transport: "http"`, `window.thisProcess: true` and the window's live scan in `watching`; 16 tools and 10 prompts. With Agent access off, and after Quit, it ran on its own with the note (2026-10-18) |
| Claude Code | not tested | stdio attached (`netdiag mcp --attach`) | — | — |
| Claude Desktop | not tested | stdio (JSON config), with or without `--attach` | — | — |

Test entries were added to a temporary configuration and removed after the test. Claude Desktop, Claude Code with `--attach`, and macOS have not been tested for this release.

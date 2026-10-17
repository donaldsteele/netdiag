# netdiag MCP reference

netdiag can be used by Claude or other AI agents through the **Model Context Protocol (MCP)**. An agent connected to netdiag can read scan results, device details, health checks and history, and can start scans. It cannot change settings, delete history, ignore MAC addresses or quit netdiag.

For a plain-language setup guide, see **Connect Claude** in the main [README](../README.md#connect-claude-ai-agent-access-via-mcp).

## Overview

netdiag offers MCP in two ways:

| Method | How it runs | Needs the netdiag window open? | Live mode | Turned on by |
|---|---|---|---|---|
| **stdio** (`netdiag mcp`) | The agent starts its own netdiag process | No | Not available | Always available |
| **HTTP** (`/mcp` on the UI server) | Shares the running netdiag window | Yes | Available | **Settings > Agent access (MCP)** (off by default) |

Both methods use the same tools, resources and prompts, and the same safety rules. Use stdio when you want Claude to run a scan on its own. Use HTTP when you want Claude to see the same scan you see in the browser.

Scans sent by an agent send real network traffic, just like the **Scan** button. Only connect agents to a network you are allowed to scan.

## Connection methods

### stdio: `netdiag mcp`

```
netdiag mcp [--store-dir DIR]
```

- Speaks MCP over standard input and output. Logs go to stderr only, so stdout carries only protocol messages.
- Settings and history are read from the same folder as the UI (see the README, "Where the data is kept"). Use `--store-dir DIR` to choose another folder for this run. If the folder cannot be opened, netdiag prints a warning to stderr and continues.
- Ctrl+C or the agent closing the connection stops the process. Scans it started are cancelled and their partial results are kept.

### HTTP: the running netdiag window

1. Start the UI on a fixed port: `netdiag ui --port 8787 --no-browser`.
2. Open **Settings**, find **Agent access (MCP)**, and turn on **Allow AI agents to connect (MCP)**. Click **Save**.
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

Claude Desktop uses stdio only. For the HTTP method, use Claude Code.

## Tools

All tools return compact JSON. List tools return pages (use `cursor` and `nextCursor`). Invalid input or an unknown scan returns an error message that says what to fix.

| Tool | What it does | Key inputs | Read-only? |
|---|---|---|---|
| `list_interfaces` | Lists usable network adapters with the suggested scan range for each. | none | Yes |
| `start_scan` | Starts a scan and waits up to `waitSec` (default 30) for it to finish. If a scan is already running, returns that scan with `alreadyRunning: true`. | `cidr` (max /22, default: suggested), `interface`, `community`, `waitSec` (0–120), `live` (HTTP only), `liveIntervalSec` | No (sends network traffic) |
| `get_scan` | Summary of a scan: counts, health, conflicts and printer alerts. Checks the running scans first, then saved history. | `scanId` | Yes |
| `cancel_scan` | Cancels a scan started in this netdiag process. Results collected so far are kept. | `scanId` | No (stops a scan) |
| `list_devices` | Paged device rows for a scan. | `scanId`, `filter` (all, printers, web, offline, conflicts, new, mdns, problems), `deviceType`, `query` (IP, MAC, name, vendor or mDNS service text such as `_ipp._tcp`), `limit` (default 50, max 200), `cursor` | Yes |
| `get_device` | Full detail for one device: ports (including the remote access ports 22, 3389 and 5900), web services, SNMP, printer supplies and alerts, and conflicts involving it. Agents cannot start remote sessions; that is a button in the netdiag window. | `scanId`, and `ip` or `mac` | Yes |
| `get_events` | Change feed of a scan started in this process: new, offline and back-online devices, printer changes, conflicts, health changes. | `scanId`, `sinceSeq` (pass the last `nextSeq`), `limit` (default 100, max 500) | Yes |
| `get_health` | Latest health report (gateway, DHCP, DNS, internet, Wi-Fi, web server). Says `none` if there is none yet. | none | Yes |
| `run_health_check` | Runs the health checks now (about 10 seconds) on the network adapter of the running scan, or of the most recent scan since netdiag was started, with the router that scan found. With no such scan it checks the adapter that carries the default route and that route's router. A router is checked only if it is on the adapter's network; otherwise the Gateway check says no router is known for this network adapter. | none | No (sends network traffic) |
| `probe_host` | Checks one device on a local subnet. Nothing is saved. | `ip` (must be on a local interface subnet), `community` | No (sends network traffic) |
| `list_history` | Saved scans, newest first. | `limit` (default 20, max 100) | Yes |
| `compare_scans` | What was added, removed and changed between two scans (up to 200 rows per list). | `baseId` (default: previous scan of the same range), `currentId` | Yes |
| `get_settings` | Current settings. The SNMP community is shown as `***`, and a password in the web server URL (`http://user:password@host/`) as `xxxxx`. | none | Yes |

**What `filter=problems` lists.** The same devices as the **Problems** filter in the netdiag window:

- a device that live mode marked offline (this computer never counts as offline),
- a device involved in a conflict,
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
| `netdiag://scans/{scanId}` | Scan summary |
| `netdiag://scans/{scanId}/devices` | First 200 device rows of the scan |
| `netdiag://scans/{scanId}/devices/{ip}` | Full detail for one device |

`resources/list` shows only the four fixed resources. Scans are not listed one by one: `resources/templates/list` shows the three `netdiag://scans/...` templates, and the scan ids to fill in come from `netdiag://history` (or the `list_history` tool) and from `start_scan`. A template works for saved scans and for scans running in this netdiag process.

## Prompts

Prompts are short playbooks. The client shows them as commands or menu items, and fills in the arguments. Each one tells the agent which tools to call and in what order.

| Prompt | Arguments | What the agent does |
|---|---|---|
| `triage_network` | `cidr` (optional, default: first interface) | Scan, check health, list problem devices, check conflicts, then suggest likely causes. |
| `diagnose_printer` | `ip` (required) | Read the printer's details, probe it, and check its supplies (low means at or below the supply threshold in Settings), alerts, IPP state, ports 9100 and 631, and conflicts. |
| `explain_conflict` | `ip` (required) | Explain the conflict type (flapping, same MAC on several IPs, duplicate name), and what the operator should do. The agent can only recommend **Ignore this MAC**; it cannot do it. |
| `device_missing` | `nameOrIp` (required) | Search the current scan and history, compare ARP and ping results, and read the offline timeline. |
| `what_changed` | `baseId`, `currentId` (both optional) | Compare two scans (default: the latest scan and the one before it) and describe what changed. |

Calling a prompt without a required argument returns an error.

## Safety model

- **Loopback only.** The HTTP endpoint listens on `127.0.0.1` and only answers when the `Host` header is the local address. Requests whose `Origin` header is not `http://127.0.0.1:<port>` or `http://localhost:<port>` are refused with `403`.
- **Off by default.** HTTP access is off until someone turns on **Agent access (MCP)** in Settings. An agent cannot turn it on: the settings endpoints are not offered as tools.
- **Read-only settings.** Agents cannot change settings, delete history, ignore MAC addresses, or quit netdiag. These actions are not available as MCP tools, and the code that would run them is kept out of the MCP package.
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
The netdiag window is not running, or it was started without `--port`, so the port changed. Start `netdiag ui --port 8787 --no-browser` and use the same port in the URL.

**HTTP: `404` with "MCP is disabled".**
Agent access is off. Open **Settings**, turn on **Allow AI agents to connect (MCP)**, and click **Save**.

**HTTP: `403`.**
The request came from a different address or Origin. Use `http://127.0.0.1:<port>/mcp` (not a LAN address or a host name).

**Windows Firewall asks about netdiag.**
The alert comes from scanning, not from MCP: a scan waits for answers from devices, such as Bonjour (mDNS) names. The HTTP method listens only on `127.0.0.1`, so it is reachable from this computer only. The stdio method opens no network port. Without an administrator password, click **Cancel**; scans still work, but Bonjour names may be missing. See "Windows shows a Windows Security Alert about netdiag" in the main README troubleshooting section.

**A scan started by an agent finds the wrong network.**
Call `list_interfaces` to see the adapters and their suggested ranges, then pass `interface` or `cidr` to `start_scan` explicitly. A VPN or virtual adapter can be picked by default.

## Tested versions

| Client | Version | Transport | Platform | Result |
|---|---|---|---|---|
| Claude Code | 2.1.296 | stdio (`claude mcp add`, then `claude mcp list`) | Windows 11 | Connected |
| Claude Code | 2.1.296 | HTTP (`--transport http` to `http://127.0.0.1:8787/mcp` with Agent access on) | Windows 11 | Connected |
| Claude Desktop | not tested | stdio (JSON config) | — | — |

Test entries were added to a temporary configuration and removed after the test. Claude Desktop and macOS have not been tested for this release.

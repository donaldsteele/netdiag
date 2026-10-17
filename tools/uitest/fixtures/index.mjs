// Deterministic fixtures shaped exactly like the JSON the netdiag server emits
// (internal/model/*.go wire types, internal/server handlers, sse.go framing).
// Values are synthetic and scrubbed: documentation-style addresses, invented
// MACs and names. Shapes follow testdata/report/golden.json and diff/*.json.

export const SCAN_ID = 'c0ffee00-0000-4000-8000-000000000001';
export const BASE_ID = 'h-20261009-1800';
export const GATEWAY = '192.168.2.1';
export const SELF_IP = '192.168.2.50';
export const CIDR = '192.168.2.0/24';
export const TOTAL_HOSTS = 254;
export const CONFLICT_ID = 'c-1';

const T0 = '2026-10-10T09:30:00Z';
const T1 = '2026-10-10T09:30:12Z';

const VENDORS = ['Apple, Inc.', 'Intel Corporate', 'Raspberry Pi Trading', 'Espressif Inc.',
  'Ubiquiti Networks', 'Microsoft Corporation', 'Dell Inc.', 'Samsung Electronics'];

const hex2 = (v) => v.toString(16).padStart(2, '0').toUpperCase();
// Locally administered, synthetic MACs derived from the address number.
const mac = (n) => `02:00:5E:10:${hex2((n >> 8) & 0xff)}:${hex2(n & 0xff)}`;

const ip = (n) => `192.168.2.${n}`;

function baseHost(n) {
  return {
    ip: ip(n), isSelf: false, alive: false, foundBy: [], rttMs: 0,
    mac: '', vendor: '', hostname: '', openPorts: [], web: [], snmp: null,
    isPrinter: false, printerReason: '', printer: null, errors: [],
    updatedAt: T1, online: false, mdns: null,
  };
}

function printerBrother() {
  return {
    model: 'HL-L2350DW', serial: 'E7Z123456', pageCount: 12345,
    consoleDisplay: ['READY'],
    supplies: [
      { description: 'Black Toner', type: 'toner', color: 'black', level: 150, max: 1000, percent: 15, unit: 'impressions', state: 'low' },
      { description: 'Drum, "Long Life"', type: 'drum', color: '', level: -2, max: -2, percent: -1, unit: '', state: 'unknown' },
    ],
    inputTrays: [{ name: 'Tray 1', mediaName: 'A4', level: 100, max: 250, status: 'ok' }],
    alerts: [{ severity: 'warning', code: 'toner-low', description: 'Toner low, replace soon' }],
    covers: [{ description: 'Front Cover', status: 'closed' }],
    source: 'snmp+ipp', state: 'idle', stateReasons: [],
  };
}

function printerEpson() {
  return {
    model: 'WF-4745', serial: 'X4K009871', pageCount: 880,
    consoleDisplay: ['Ink low'], supplies: [
      { description: 'Cyan Ink', type: 'ink', color: 'cyan', level: 0, max: 100, percent: 0, unit: 'percent', state: 'empty' },
      { description: 'Magenta Ink', type: 'ink', color: 'magenta', level: 70, max: 100, percent: 70, unit: 'percent', state: 'ok' },
    ],
    inputTrays: [], alerts: [], covers: [], source: 'snmp', state: 'idle', stateReasons: [],
  };
}

// Device classification per address (all 12 backend types appear); others are unknown.
const DEVICE_TYPES = {
  1: ['gateway', 'default gateway'], 50: ['self', 'this computer'],
  10: ['printer', 'hrDeviceType=printer'], 20: ['printer', 'hrDeviceType=printer'],
  30: ['nas', 'vendor Netgear ReadyNAS, port 5000'], 31: ['camera', 'RTSP port 554 open'],
  200: ['server', 'HTTP on port 8080'], 4: ['computer', 'vendor Dell, SMB ports'],
  12: ['phone', 'vendor Apple, no open ports'], 16: ['tv', 'mDNS _googlecast._tcp'],
  24: ['network', 'vendor Ubiquiti, SNMP sysDescr'], 28: ['iot', 'vendor Espressif'],
};

// hosts(): the full 254-address /24 as the server reports it after a scan.
export function hosts() {
  const out = [];
  for (let n = 1; n <= TOTAL_HOSTS; n++) {
    const h = baseHost(n);
    const alive = n === 1 || n === 50 || n % 4 === 0;
    if (n === 50) {
      h.isSelf = true; h.alive = true; h.online = true; h.mac = 'AA:BB:CC:00:11:22';
      h.foundBy = ['icmp']; h.rttMs = 0;
    } else if (alive) {
      h.alive = true; h.online = true; h.foundBy = ['icmp', 'arp'];
      h.rttMs = Math.round((0.4 + (n % 17) * 0.9) * 10) / 10;
      h.mac = mac(n);
      h.vendor = VENDORS[n % VENDORS.length];
      h.lastSeen = T1; h.firstSeen = T0;
    }
    if (n === 1) { h.hostname = 'router.lan'; h.vendor = 'Ubiquiti Networks'; h.mac = 'C0:FF:EE:00:00:01'; }
    if (n === 10) {
      h.foundBy = ['icmp', 'arp', 'tcp']; h.openPorts = [80, 443, 9100]; h.isPrinter = true;
      h.mac = '00:1B:A9:12:34:56'; h.vendor = 'Brother Industries';
      h.printerReason = 'hrDeviceType=printer'; h.printer = printerBrother();
      h.web = [{ port: 80, scheme: 'http', url: 'http://192.168.2.10/', status: 200, title: 'Brother HL-L2350DW', server: 'debut/1.30', tls: null, error: '' }];
      h.snmp = { version: 'v2c', sysDescr: 'Brother HL-L2350DW series', sysName: 'BRW001BA9123456', hrDeviceStatus: 'running',
        hrPrinterStatus: 'idle', hrPrinterDetectedErrorState: ['lowToner'] };
    }
    if (n === 20) {
      h.foundBy = ['icmp', 'tcp']; h.openPorts = [631, 9100]; h.isPrinter = true;
      h.mac = '64:EB:8C:20:00:20'; h.vendor = 'Seiko Epson';
      h.printerReason = 'hrDeviceType=printer'; h.printer = printerEpson();
    }
    if (n === 30 || n === 31) {
      // Two addresses answering with one MAC: a mac_multi_ip conflict.
      h.mac = '00:1B:A9:AA:00:30'; h.vendor = 'Netgear'; h.conflicts = [CONFLICT_ID];
    }
    if (alive && n % 8 === 0 && n !== 10 && n !== 20) {
      h.openPorts = [80, 443];
      h.web = [{ port: 80, scheme: 'http', url: `http://${ip(n)}/`, status: 200, title: `Device ${n}`, server: 'nginx', tls: null, error: '' }];
    }
    if (n === 200) {
      h.vendor = 'VMware, Inc.'; h.mac = '00:50:56:AB:CD:EF'; h.openPorts = [8080];
      h.web = [{ port: 8080, scheme: 'http', url: 'http://192.168.2.200:8080/', status: 0, title: '', server: '', tls: null, error: 'connection reset' }];
    }
    if (n === 4) {
      // A Windows PC that offers SSH, Remote Desktop and VNC screen sharing.
      h.foundBy = ['icmp', 'arp', 'tcp']; h.openPorts = [22, 3389, 5900]; h.hostname = 'DESKTOP-FRONT'; h.vendor = 'Dell Inc.';
    }
    if (n === 123) { h.errors = ['snmp: timeout']; }
    const dt = DEVICE_TYPES[n];
    h.deviceType = dt ? dt[0] : 'unknown';
    h.deviceTypeReason = dt ? dt[1] : 'no identifying signals';
    out.push(h);
  }
  return out;
}

// What this computer can open remote sessions with (a Windows PC).
export function remoteCaps() {
  return { platform: 'windows', rdp: true, rdpApp: 'Remote Desktop Connection', vnc: false };
}

export function conflict(resolved = false) {
  return {
    id: CONFLICT_ID, kind: 'mac_multi_ip', severity: 'warn',
    ips: [ip(30), ip(31)], macs: ['00:1B:A9:AA:00:30'], name: '',
    firstSeen: T0, lastSeen: T1,
    detail: 'MAC 00:1B:A9:AA:00:30 answers for 192.168.2.30 and 192.168.2.31',
  };
}

export function health() {
  return {
    checkedAt: T1,
    checks: [
      { id: 'gateway', label: 'Default gateway', status: 'ok', detail: `Reachable at ${GATEWAY}`, latencyMs: 1.4 },
      { id: 'dhcp', label: 'DHCP', status: 'ok', detail: 'Lease active, 23h remaining', latencyMs: 0 },
      { id: 'dns', label: 'DNS', status: 'ok', detail: 'Resolved in 18 ms', latencyMs: 18.2 },
      { id: 'webserver', label: 'Web server', status: 'unavailable', detail: 'No web server URL configured', latencyMs: 0 },
      { id: 'internet', label: 'Internet', status: 'ok', detail: 'Reachable', latencyMs: 42.7 },
      { id: 'wifi', label: 'Wi-Fi', status: 'warn', detail: 'Signal 48% on 5 GHz', latencyMs: 0 },
    ],
  };
}

export function notice(n = 1) {
  if (n === 1) {
    return { id: 'n-1', trigger: 'supplyLow', severity: 'warn', ip: ip(10), title: 'Toner low',
      message: 'Black Toner is at 15% on HL-L2350DW', at: T1, key: `supplyLow:${ip(10)}:Black Toner` };
  }
  return { id: 'n-2', trigger: 'deviceOffline', severity: 'warn', ip: ip(200), title: 'Device offline',
    message: `VMware, Inc. at ${ip(200)} stopped responding`, at: new Date().toISOString(), key: `deviceOffline:${ip(200)}` };
}

export function interfaces() {
  return [
    { name: 'Wi-Fi', ip: SELF_IP, cidr: CIDR, mac: 'AA:BB:CC:00:11:22', isDefault: true, suggested: CIDR },
    { name: 'Ethernet', ip: '10.0.0.25', cidr: '10.0.0.0/24', mac: 'AA:BB:CC:00:11:33', isDefault: false, suggested: '10.0.0.0/24' },
  ];
}

export function settings() {
  return {
    schemaVersion: 1, community: 'public', printerStatusPollSec: 5,
    webServer: { url: '', mustContain: '', expectStatus: 200 },
    internetCheckURL: '',
    alerts: {
      sound: false, desktop: false, cooldownSec: 300, supplyThresholdPct: 15,
      triggers: { printerOffline: true, deviceOffline: true, supplyLow: true, printerAlert: true,
        newDevice: true, ipConflict: true, healthFailed: true },
    },
    ignoredConflictMACs: [],
    mcp: { enabled: false },
  };
}

export function history() {
  return [
    { id: SCAN_ID, cidr: CIDR, startedAt: T0, finishedAt: T1, state: 'done', live: false,
      stats: { scanned: TOTAL_HOSTS, alive: expectedStats().alive, printers: 2, web: 9, conflicts: 1, mdns: 0 } },
    { id: BASE_ID, cidr: CIDR, startedAt: '2026-10-09T18:00:00Z', finishedAt: '2026-10-09T18:00:09Z', state: 'done', live: true,
      stats: { scanned: TOTAL_HOSTS, alive: 62, printers: 2, web: 8 } },
    { id: 'h-corrupt', cidr: '', startedAt: '', finishedAt: '', state: '', live: false, stats: {}, error: 'unreadable' },
  ];
}

// compare(baseId, currentId): a Diff with one added, one removed and two changed hosts.
export function compare(baseId = BASE_ID, currentId = SCAN_ID) {
  const ref = (id) => ({ id, cidr: CIDR, startedAt: id === SCAN_ID ? T0 : '2026-10-09T18:00:00Z',
    finishedAt: id === SCAN_ID ? T1 : '2026-10-09T18:00:09Z' });
  const all = hosts();
  return {
    base: ref(baseId), current: ref(currentId),
    added: [all[253]],
    removed: [{ ...baseHost(99), alive: true, online: true, mac: '00:1B:A9:00:00:99', vendor: 'Acme Corp' }],
    changed: [
      { ip: ip(10), mac: '00:1B:A9:12:34:56', vendor: 'Brother Industries', changes: [
        { field: 'printer.state', from: 'processing', to: 'stopped' },
        { field: 'supply:Black Toner', from: '40%', to: '15%' } ] },
      { ip: ip(200), mac: '00:50:56:AB:CD:EF', vendor: 'VMware, Inc.', changes: [
        { field: 'online', from: 'true', to: 'false' } ] },
    ],
    summary: { added: 1, removed: 1, changed: 2 },
  };
}

// expectedStats(): counts the UI derives from hosts (stats() in app.js).
export function expectedStats(list = hosts()) {
  return {
    alive: list.filter((h) => h.alive).length,
    printers: list.filter((h) => h.isPrinter).length,
    web: list.filter((h) => (h.web || []).length).length,
  };
}

export function scanResult({ id = SCAN_ID, state = 'done', list = hosts(), withHealth = true } = {}) {
  return {
    id, request: { cidr: CIDR, interface: 'Wi-Fi', community: 'public', ports: [80, 443, 8080, 8443, 9100, 631, 515], live: state === 'live' },
    startedAt: T0, finishedAt: state === 'done' ? T1 : '', state,
    self: { ip: SELF_IP, mac: 'AA:BB:CC:00:11:22', interface: 'Wi-Fi' },
    gateway: GATEWAY, hosts: list,
    stats: { scanned: TOTAL_HOSTS, ...expectedStats(list), conflicts: 1, mdns: 0 },
    health: withHealth ? health() : null,
    conflicts: [conflict()], notices: [notice(1)], schemaVersion: 2,
  };
}

// Frames are {type, data}; sseBody() turns them into the wire format sse.go writes.
// The leading "retry" field keeps Chromium from re-requesting the finite mock stream.
export function sseBody(frames) {
  let out = 'retry: 600000\n\n: connected\n\n';
  for (const f of frames) out += `event: ${f.type}\ndata: ${JSON.stringify(f.data)}\n\n`;
  return out;
}

// Scan run in "done" mode: pipeline stages, one host event per address, then done.
export function scanFrames() {
  const list = hosts();
  const alive = expectedStats(list).alive;
  const frames = [
    { type: 'stage', data: { stage: 'icmp' } },
    { type: 'progress', data: { stage: 'icmp', done: TOTAL_HOSTS, total: TOTAL_HOSTS } },
    { type: 'stage', data: { stage: 'arp' } },
    { type: 'progress', data: { stage: 'arp', done: TOTAL_HOSTS, total: TOTAL_HOSTS } },
    { type: 'stage', data: { stage: 'probe' } },
    { type: 'progress', data: { stage: 'probe', done: alive, total: alive } },
  ];
  for (const h of list) frames.push({ type: 'host', data: h });
  frames.push({ type: 'conflict', data: { conflict: conflict(), resolved: false } });
  frames.push({ type: 'notice', data: notice(1) });
  frames.push({ type: 'health', data: health() });
  frames.push({ type: 'done', data: scanResult({ list }) });
  return frames;
}

// Live run: snapshot after the initial scan (253 addresses), then a new device,
// a device going offline (host + presence), a conflict, a notice and health.
export function liveFrames() {
  const all = hosts();
  const initial = all.slice(0, 253);
  const now = new Date().toISOString();
  const liveStatus = (cycle, newHosts) => ({
    cycle, unseen: 1, newHosts, online: 64, offline: 0, intervalSec: 15,
    lastCheckAt: now, nextCheckAt: new Date(Date.now() + 15000).toISOString(),
  });
  const fresh = { ...all[253], isNew: true, firstSeen: now, lastSeen: now, online: true, alive: true,
    mac: '00:1B:A9:FE:00:FE', vendor: 'Espressif Inc.', foundBy: ['icmp', 'arp'] };
  const offline = { ...all[199], online: false, alive: true, offlineSince: now, missedChecks: 2, lastSeen: T1 };
  return [
    { type: 'snapshot', data: scanResult({ id: SCAN_ID, state: 'live', list: initial, withHealth: true }) },
    { type: 'live', data: liveStatus(1, 0) },
    { type: 'host', data: fresh },
    { type: 'live', data: liveStatus(2, 1) },
    { type: 'host', data: offline },
    { type: 'presence', data: { ip: ip(200), online: false, at: now, reason: 'no reply to 2 checks' } },
    { type: 'conflict', data: { conflict: conflict(), resolved: false } },
    { type: 'notice', data: notice(2) },
    { type: 'health', data: health() },
  ];
}

// Deterministic fixtures shaped exactly like the JSON the netdiag server emits
// (internal/model/*.go wire types, internal/server handlers, sse.go framing).
// Values are synthetic and scrubbed: documentation-style addresses, invented
// MACs and names. Shapes follow testdata/report/golden.json and diff/*.json.

export const SCAN_ID = 'c0ffee00-0000-4000-8000-000000000001';
export const BASE_ID = 'h-20261009-1800';
export const GATEWAY = '192.168.2.1';
export const SELF_IP = '192.168.2.50';
export const CIDR = '192.168.2.0/24';
// The id the health checks give this network, in the form model.NetworkID
// makes; the "This network has no internet" switch is kept per network id
// (v0.6-D15).
export const NETWORK = 'n-5f2c0a9d3e71';
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

// noStatusPrinter(): a printer found by its print port that reports no
// status (no SNMP, no IPP), with a web page on its own address. The drawer
// shows the printer-without-status advice with Open its page.
export const NOSTATUS_IP = ip(36);
export function noStatusPrinter() {
  return { ...baseHost(36), alive: true, online: true, foundBy: ['icmp', 'tcp'], mac: mac(36), vendor: 'Kyocera',
    openPorts: [80, 9100], isPrinter: true, printerReason: 'port 9100 (raw printing) open', firstSeen: T0, lastSeen: T1,
    deviceType: 'printer', deviceTypeReason: 'port 9100 (raw printing) open',
    web: [{ port: 80, scheme: 'http', url: `http://${ip(36)}/`, status: 200, title: 'Printer', server: '', tls: null, error: '' }],
    advice: advice("This printer doesn't report its status, so netdiag can only see whether it answers.",
      ["Turn on SNMP in the printer's settings page (often under Network), or ask whoever looks after it."],
      [{ kind: 'openDevicePage', label: 'Open its page', target: `http://${ip(36)}/` }]) };
}

// What this computer can open remote sessions with (a Windows PC).
export function remoteCaps() {
  return { platform: 'windows', rdp: true, rdpApp: 'Remote Desktop Connection', vnc: false };
}

export function conflict(resolved = false) {
  const detail = 'MAC 00:1B:A9:AA:00:30 answers for 192.168.2.30 and 192.168.2.31';
  return {
    id: CONFLICT_ID, kind: 'mac_multi_ip', severity: 'warn',
    ips: [ip(30), ip(31)], macs: ['00:1B:A9:AA:00:30'], name: '',
    firstSeen: T0, lastSeen: T1, detail,
    advice: advice("One device answers for several addresses. This is normal for Wi-Fi extenders, mesh points and some routers.",
      ["If this is an extender or mesh point, you can ignore it."], [], detail),
  };
}

// The address of the mac_flap fixtures, its two MACs, and the address
// where the second device also answers.
export const FLAP_IP = ip(44);
export const FLAP_MACS = ['24:6F:28:AA:BB:CC', '3C:52:82:11:22:33'];
export const FLAP_ID = 'c-flap';

// The makers the scan names for the two MACs (Conflict.Vendors).
export const FLAP_VENDORS = { [FLAP_MACS[0]]: 'Espressif Inc.', [FLAP_MACS[1]]: 'Hewlett Packard' };

// flapConflict(alternations): a mac_flap conflict at FLAP_IP. With 2 or
// more alternations the swap was seen ("Conflict"); with fewer it is a
// "Possible conflict" (internal/advice SwapSeen).
export function flapConflict(alternations = 2) {
  const detail = `${FLAP_IP} keeps alternating between 2 MAC addresses (${FLAP_MACS.join(', ')}); two devices likely share this IP.`;
  const a = alternations >= 2
    ? advice("Two devices are taking turns using 192.168.2.44, so both work badly.",
      ["Open this address in netdiag and compare the two devices it shows (Device A and Device B).", "Restart one of them so it gets a new address.",
        "If it keeps happening, ask whoever looks after the router to reserve an address for one of them."], [showDevice(FLAP_IP)], detail)
    : advice("Possible conflict: the device answering at 192.168.2.44 changed.",
      ["If you just replaced a device, this is expected.", "If it happens again, open this address in netdiag to see both devices."], [], detail);
  return { id: FLAP_ID, kind: 'mac_flap', severity: 'fail', ips: [FLAP_IP], macs: [...FLAP_MACS], name: '',
    firstSeen: T0, lastSeen: T1, detail, vendors: { ...FLAP_VENDORS }, advice: a };
}

// flapHosts(alternations, alsoAt): hosts() with FLAP_IP's MAC history
// showing that many alternations (10:02, 10:07, 10:12 ... in UTC), and,
// unless alsoAt is false, the second MAC also answering at .45.
export function flapHosts(alternations = 2, alsoAt = true) {
  const list = hosts();
  const at = (i) => `2026-10-10T10:${String(2 + 5 * i).padStart(2, '0')}:00Z`;
  const hist = [];
  for (let i = 0; i <= alternations; i++) hist.push({ mac: FLAP_MACS[i % 2], at: at(i) });
  const flap = list.find((h) => h.ip === FLAP_IP);
  const mac = hist[hist.length - 1].mac;
  Object.assign(flap, { mac, vendor: FLAP_VENDORS[mac], macHistory: hist, conflicts: [FLAP_ID] });
  if (alsoAt) {
    const other = list.find((h) => h.ip === ip(45));
    Object.assign(other, { alive: true, online: true, mac: FLAP_MACS[1], vendor: FLAP_VENDORS[FLAP_MACS[1]], foundBy: ['arp'] });
  }
  return list;
}

// advice(): a model.Advice as internal/advice builds it. Meanings and steps
// are written in double quotes so TestUITestFixtureAdvice (internal/advice)
// can check each one is what the catalogue really says.
export function advice(meaning, steps = [], actions = [], technical = '') {
  const a = { meaning };
  if (steps.length) a.steps = steps;
  if (actions.length) a.actions = actions;
  if (technical) a.technical = technical;
  return a;
}
const RECHECK = { kind: 'recheck', label: 'Check again' };
const settingsAction = (target) => ({ kind: 'openSettings', label: 'Open Settings', target });
const showDevice = (target) => ({ kind: 'showDevice', label: 'Show device', target });

// Web server URL used by the web server health fixtures.
export const WEB_URL = 'https://intranet.local/';

// SHA-256 of the self-signed certificate the 'untrusted' web server presents.
export const CERT_SHA256 = '5d41402abc4b2a76b9719d911017c592' + '7d793fcb0ab1e6b2b4a2d1c0f9e8d7c6';

// The web server check in each state: 'unset' (no URL in Settings), 'ok',
// 'untrusted' (self-signed certificate this computer does not trust), 'down'
// (HTTP 503) or 'unset-old' (unset, as a report saved before v0.6 has it,
// without reason or advice). url is the address the check reports in
// data.url (the server hides any password).
export function webCheck(state = 'unset', url = WEB_URL) {
  const base = { id: 'webserver', label: 'Web server' };
  if (state === 'ok') {
    return { ...base, status: 'ok', detail: 'HTTP 200 in 12 ms', latencyMs: 12.3,
      data: { configured: true, url, status: 200 } };
  }
  if (state === 'untrusted') {
    const detail = 'the certificate is not trusted: self-signed or from a private authority';
    return { ...base, status: 'fail', detail, latencyMs: 0,
      data: { configured: true, url, certSHA256: CERT_SHA256, certSubject: 'intranet.local',
        certExpires: '2027-10-01T00:00:00Z', reason: 'tls' },
      advice: advice("The web server's certificate isn't trusted.",
        ["If this is your own server on this network, open Health in netdiag and press Trust this certificate on the Web server card.",
          "Otherwise, ask whoever looks after it."], [], detail) };
  }
  if (state === 'down') {
    const detail = 'HTTP 503 (expected 200)';
    return { ...base, status: 'fail', detail, latencyMs: 0, data: { configured: true, url, status: 503, reason: 'status' },
      advice: advice("The web server isn't answering as expected (HTTP 503).",
        ["Check that the server is switched on.", "Ask whoever looks after it."], [settingsAction('webserver'), RECHECK], detail) };
  }
  if (state === 'unset-old') {
    // As a report saved before v0.6 has it: no reason, no advice.
    return { ...base, status: 'info', detail: 'not configured', latencyMs: 0, data: { configured: false } };
  }
  return { ...base, status: 'info', detail: 'not configured', latencyMs: 0,
    data: { configured: false, reason: 'not_set' },
    advice: advice("No web server is set, so this check is skipped.", [], [settingsAction('webserver')], 'not configured') };
}

function wifiWeak(pct = 48) {
  const detail = `Signal ${pct}% on 5 GHz`;
  return { id: 'wifi', label: 'Wi-Fi', status: 'warn', detail, latencyMs: 0, data: { signalPct: pct, reason: 'weak' },
    advice: advice(pct === 32 ? "The Wi-Fi signal is weak (32%)." : "The Wi-Fi signal is weak (48%).", ["Move closer to the router, or use a network cable."], [RECHECK], detail) };
}

// web: the web server check state and webURL its reported address (see webCheck).
// state: 'default' (Wi-Fi weak, the rest fine); 'all' (every check has
// something to say, each with its advice); 'offline' (a local network
// without internet), where noInternet says whether the "This network has
// no internet" switch (v0.6-D15) was on for NETWORK when the checks ran;
// 'dns' (only the name check fails); 'nogateway' (no router is known, so
// that check could not run); 'location' (Windows hides the Wi-Fi details).
// Every report names the network it checked (NETWORK).
export function health({ web = 'unset', webURL = WEB_URL, state = 'default', noInternet = false } = {}) {
  if (state === 'all') return healthAll();
  if (state === 'offline') return healthOffline(noInternet);
  if (state === 'dns') {
    // Only the name check fails (a warning on the card, not urgent), and
    // Wi-Fi is fine.
    const h = health({ web, webURL });
    const detail = 'DNS server 192.168.2.1 did not answer (i/o timeout)';
    h.checks[2] = { id: 'dns', label: 'Name lookup (DNS)', status: 'fail', detail, latencyMs: 0, data: { reason: 'no_reply' },
      advice: advice("No name server answers, so this computer can't look up names.",
        ["Restart the router.", "If you use a VPN, disconnect it and check again."], [RECHECK], detail) };
    h.checks[5] = { id: 'wifi', label: 'Wi-Fi', status: 'ok', detail: 'ShopFloor, signal 82%, channel 36 (5 GHz)', latencyMs: 0 };
    return h;
  }
  if (state === 'nogateway') {
    // No router is known for the adapter: the check could not run, a
    // warning on the card. Wi-Fi is fine.
    const h = health({ web, webURL });
    const detail = 'no router is known for this network adapter (Ethernet)';
    h.checks[0] = { id: 'gateway', label: 'Router', status: 'unavailable', detail, latencyMs: 0, data: { reason: 'no_gateway' },
      advice: advice("netdiag doesn't know the router of this network, so it can't check it.",
        ["Check that this computer is connected to the network.", "If it is, check again in a minute."], [RECHECK], detail) };
    h.checks[5] = { id: 'wifi', label: 'Wi-Fi', status: 'ok', detail: 'ShopFloor, signal 82%, channel 36 (5 GHz)', latencyMs: 0 };
    return h;
  }
  if (state === 'location') {
    // Windows hides Wi-Fi details until Location is on.
    const h = health({ web, webURL });
    const detail = 'Windows blocks Wi-Fi details until Location is on: Settings > Privacy & security > Location, turn on Location services and Let desktop apps access your location';
    h.checks[5] = { id: 'wifi', label: 'Wi-Fi', status: 'unavailable', detail, latencyMs: 0, data: { reason: 'hidden_location' },
      advice: advice("Windows hides Wi-Fi details until Location is turned on. The other checks still work.", [],
        [{ kind: 'osSettings', label: 'Open Location settings', target: 'location' }], detail) };
    return h;
  }
  return {
    checkedAt: T1, network: NETWORK,
    checks: [
      { id: 'gateway', label: 'Router', status: 'ok', detail: `Reachable at ${GATEWAY}`, latencyMs: 1.4 },
      { id: 'dhcp', label: 'Getting an address (DHCP)', status: 'ok', detail: 'Lease active, 23h remaining', latencyMs: 0 },
      { id: 'dns', label: 'Name lookup (DNS)', status: 'ok', detail: 'Resolved in 18 ms', latencyMs: 18.2 },
      webCheck(web, webURL),
      { id: 'internet', label: 'Internet', status: 'ok', detail: 'Reachable', latencyMs: 42.7 },
      wifiWeak(),
    ],
  };
}

// healthAll(): all six checks with a result that is not ok.
export function healthAll() {
  const gw = `${GATEWAY} did not reply to ping or on ports 80 and 443`;
  const dhcp = 'self-assigned 169.254.x.x address: no DHCP server answered';
  const dns = 'could not resolve connectivitycheck.gstatic.com, but DNS server 192.168.2.1 answers: no internet connection, or the DNS server cannot reach the internet (i/o timeout)';
  const inet = 'internet check got no response (local checks are unaffected)';
  return {
    checkedAt: T1, network: NETWORK,
    checks: [
      { id: 'gateway', label: 'Router', status: 'fail', detail: gw, latencyMs: 0, data: { reason: 'no_reply' },
        advice: advice("This computer can't reach your router.",
          ["Check that the router is switched on and its lights look normal.", "Check this computer's network cable or Wi-Fi.",
            "If both look fine, restart the router."], [RECHECK], gw) },
      { id: 'dhcp', label: 'Getting an address (DHCP)', status: 'fail', detail: dhcp, latencyMs: 0, data: { enabled: false, apipa: true, reason: 'apipa' },
        advice: advice("This computer didn't get an address from the router, so it can't talk to other devices.",
          ["Turn Wi-Fi off and on again, or unplug the network cable and plug it back in.", "If that doesn't help, restart the router."],
          [RECHECK], dhcp) },
      dnsNoInternet(false, dns),
      webCheck('down'),
      { id: 'internet', label: 'Internet', status: 'info', detail: inet, latencyMs: 0, data: { reason: 'no_response' },
        advice: advice("netdiag couldn't reach the internet. Devices on the local network still work.",
          ["If this network should have internet, restart the router or modem."], [RECHECK], inet) },
      wifiWeak(32),
    ],
  };
}

function dnsNoInternet(noInternet, detail = 'could not resolve connectivitycheck.gstatic.com, but DNS server 192.168.2.1 answers: no internet connection, or the DNS server cannot reach the internet (i/o timeout)') {
  if (noInternet) {
    return { id: 'dns', label: 'Name lookup (DNS)', status: 'info', detail, latencyMs: 0, data: { reason: 'no_internet', noInternet: true },
      advice: advice("This network is set up without internet, so internet names can't be looked up. That is expected.",
        ["If this network has internet now, turn off \"This network has no internet\" in Settings."], [settingsAction('noInternet')], detail) };
  }
  return { id: 'dns', label: 'Name lookup (DNS)', status: 'warn', detail, latencyMs: 0, data: { reason: 'no_internet' },
    advice: advice("Your local network works, but internet names can't be looked up. Usually this means there is no internet connection right now.",
      ["If this network should have internet, restart the router or modem.",
        "If this network has no internet on purpose, turn on \"This network has no internet\" in Settings."],
      [RECHECK, settingsAction('noInternet')], detail) };
}

// healthOffline(noInternet): a local network with no internet. Without the
// switch the name check warns; with it, every check is fine or a note.
export function healthOffline(noInternet = false) {
  const inet = 'internet check got no response (local checks are unaffected)';
  const internet = noInternet
    ? { id: 'internet', label: 'Internet', status: 'info', detail: inet, latencyMs: 0, data: { reason: 'no_response', noInternet: true },
      advice: advice("This network is set up without internet, so this check is only for information.", [], [settingsAction('noInternet')], inet) }
    : { id: 'internet', label: 'Internet', status: 'info', detail: inet, latencyMs: 0, data: { reason: 'no_response' },
      advice: advice("netdiag couldn't reach the internet. Devices on the local network still work.",
        ["If this network should have internet, restart the router or modem."], [RECHECK], inet) };
  return {
    checkedAt: T1, network: NETWORK,
    checks: [
      { id: 'gateway', label: 'Router', status: 'ok', detail: `${GATEWAY} replied in 2 ms`, latencyMs: 1.9 },
      { id: 'dhcp', label: 'Getting an address (DHCP)', status: 'ok', detail: 'address obtained by DHCP from 192.168.2.1', latencyMs: 0 },
      dnsNoInternet(noInternet),
      webCheck('unset'),
      internet,
      { id: 'wifi', label: 'Wi-Fi', status: 'ok', detail: 'ShopFloor, signal 82%, channel 36 (5 GHz)', latencyMs: 0 },
    ],
  };
}

// Keys and severities follow notify.Key and the severity map (deviceOffline
// is info); TestUITestFixtureKeys in internal/notify checks them.
export function notice(n = 1) {
  if (n === 1) {
    const message = 'Black Toner is at 15% on HL-L2350DW';
    return { id: 'n-1', trigger: 'supplyLow', severity: 'warn', ip: ip(10), mac: '00:1B:A9:12:34:56', title: 'Toner low',
      message, at: T1, key: 'supplyLow|00:1B:A9:12:34:56|1c3936d3', detail: 'Black Toner',
      advice: advice("Black Toner is at 15%.", ["Order a replacement soon."], [showDevice(ip(10))], message) };
  }
  const message = `VMware, Inc. at ${ip(200)} stopped responding`;
  return { id: 'n-2', trigger: 'deviceOffline', severity: 'info', ip: ip(200), mac: '00:50:56:AB:CD:EF', title: 'Device offline',
    message, at: new Date(Date.now() - 12 * 60000).toISOString(),
    key: 'deviceOffline|00:50:56:AB:CD:EF|',
    advice: advice("The device at 192.168.2.200 stopped answering.",
      ["Check that it is switched on and connected.", "Phones and laptops stop answering when they sleep; that can be normal."],
      [showDevice(ip(200))], message) };
}

// fixedNotice(n): the OK notice that closes notice(n): same key, ok, openedAt
// set to the fault's time, 12 minutes later for notice(2).
export function fixedNotice(n = 2) {
  const f = notice(n);
  const titles = { supplyLow: 'Supply OK again', deviceOffline: 'Device back online' };
  const at = n === 1 ? new Date(Date.parse(T1) + 12 * 60000).toISOString() : new Date().toISOString();
  return { id: `n-${n + 10}`, trigger: f.trigger, severity: 'info', ip: f.ip, mac: f.mac, title: titles[f.trigger],
    message: n === 1 ? 'Black Toner at 60% on HL-L2350DW.' : `VMware, Inc. at ${ip(200)} answers again.`,
    at, key: f.key, ok: true, openedAt: f.at };
}

// The Problems right now card (GET /api/problems, internal/problems
// NewView). problemItems(): what problems.Current gives for hosts() and
// conflict() with the default Alerts settings (TestUITestFixtureProblems in
// internal/problems checks the keys and words); healthProblems(report)
// mirrors its health part: a failed check is a problem (critical for the
// gateway), one that could not run a warning, a warning a note.
const BROTHER = { ip: ip(10), mac: '00:1B:A9:12:34:56', name: 'BRW001BA9123456' };
export function problemItems() {
  return [
    { key: 'printerAlert|00:1B:A9:12:34:56|toner-low|ae7828e7', kind: 'printerAlert', severity: 'warning', title: 'Printer alert',
      detail: 'Toner low, replace soon', device: { ...BROTHER } },
    { key: 'supplyLow|00:1B:A9:12:34:56|1c3936d3', kind: 'supplyLow', severity: 'warning', title: 'Supply low',
      detail: 'Black Toner at 15%', device: { ...BROTHER }, advice: notice(1).advice },
    { key: 'supplyLow|64:EB:8C:20:00:20|41dba9b9', kind: 'supplyLow', severity: 'warning', title: 'Supply low',
      detail: 'Cyan Ink at 0%', device: { ip: ip(20), mac: '64:EB:8C:20:00:20', name: 'WF-4745' } },
    { key: 'ipConflict|mac_multi_ip|00:1B:A9:AA:00:30', kind: 'ipConflict', severity: 'warning', title: 'Possible conflict',
      detail: `${ip(30)}, ${ip(31)}`, device: { ip: ip(30), mac: '00:1B:A9:AA:00:30' }, advice: conflict().advice },
  ];
}
// The card names a check by its plain name (model.HealthLabel).
const CHECK_NAMES = { gateway: 'Router', dhcp: 'Getting an address (DHCP)', dns: 'Name lookup (DNS)', internet: 'Internet', wifi: 'Wi-Fi', webserver: 'Web server' };
export function healthProblems(report) {
  const out = [];
  for (const c of (report && report.checks) || []) {
    const done = c.status !== 'unavailable' || ((c.id === 'wifi' || c.id === 'dhcp') && (c.data || {}).reason !== 'timeout');
    let severity, title;
    if (c.status === 'fail') { severity = c.id === 'gateway' ? 'critical' : 'warning'; title = 'Health check failed'; }
    else if (!done) { severity = 'warning'; title = 'Health check could not run'; }
    else if (c.status === 'warn') { severity = 'info'; title = 'Health check warning'; }
    else if (c.status === 'unavailable') { severity = 'info'; title = 'Health check could not run'; }
    else continue;
    const p = { key: 'healthFailed||' + c.id, kind: 'healthFailed', severity, title, detail: CHECK_NAMES[c.id] || c.label || c.id, check: c.id };
    if (c.advice) p.advice = c.advice;
    out.push(p);
  }
  return out;
}
// fixedItems(): problems the ledger saw fixed today: the device of
// notice(2), back after 12 minutes, and the DNS check. Each keeps the open
// problem's title, as Ledger.Apply does: what was fixed.
export function fixedItems() {
  const at = Date.now() - 60000;
  return [
    { key: 'deviceOffline|00:50:56:AB:CD:EF|', kind: 'deviceOffline', title: 'Device offline', device: { ip: ip(200) },
      openedAt: new Date(at - 12 * 60000).toISOString(), fixedAt: new Date(at).toISOString(), downForSec: 720 },
    { key: 'healthFailed||dns', kind: 'healthFailed', title: 'Health check failed', detail: 'Name lookup (DNS)',
      openedAt: new Date(at - 47 * 60000).toISOString(), fixedAt: new Date(at - 40 * 60000).toISOString(), downForSec: 420 },
  ];
}
const RANK = { critical: 0, warning: 1, info: 2 };
// problemsView(): the GET /api/problems answer for these items, most urgent
// first; everything is fine when there is a health report, the storm guard
// does not hold and nothing is a warning or worse. checkedAt is now unless
// given, so the card's time has no day.
export function problemsView({ open = [], fixed = [], source = 'latest', watching = false, checkedAt, hasHealth = true,
  held = false, heldReason = '', heldSince = '' } = {}) {
  const sorted = [...open].sort((a, b) => RANK[a.severity] - RANK[b.severity]);
  const v = { checkedAt: checkedAt || new Date().toISOString(), source, watching,
    everythingFine: hasHealth && !held && sorted.every((p) => p.severity === 'info'), open: sorted, fixed };
  if (held) Object.assign(v, { held, heldReason, heldSince });
  return v;
}
// someProblemsView(): six items in three severities (a dead router, the four
// warnings of the fixture scan, a weak Wi-Fi note), as GET /api/problems
// sends them.
export function someProblemsView(extra = {}) {
  const all = healthAll().checks;
  return problemsView({ open: [...problemItems(), ...healthProblems({ checks: [all[5], all[0]] })], ...extra });
}

// The storm guard (v0.6 F9c): a live status while it holds, and its local
// netdiagLink notice (reason link or network_changed; the advice is the
// catalogue's, TestUITestFixtureAdvice). The two reasons' notices have
// their own ids, as when the guard raises link to network_changed under
// one key. HELD_SINCE is when it began.
export const HELD_SINCE = '2026-10-10T09:40:00Z';
export function heldStatus(reason = 'link') {
  return { cycle: 3, unseen: 1, newHosts: 0, online: 64, offline: 0, intervalSec: 15,
    lastCheckAt: new Date().toISOString(), nextCheckAt: new Date(Date.now() + 15000).toISOString(),
    held: true, heldReason: reason, heldSince: HELD_SINCE, network: NETWORK };
}
export function linkNotice(reason = 'link') {
  const key = 'netdiagLink||' + NETWORK;
  if (reason === 'network_changed') {
    const message = 'netdiag stopped its alerts for the network it was watching. Press Restart watching to watch this one.';
    return { id: 'n-32', trigger: 'netdiagLink', severity: 'warn', ip: '', title: 'This computer is on a different network',
      message, at: HELD_SINCE, key, code: reason,
      advice: advice("This computer is on a different network now, so netdiag stopped the alerts for the network it was watching.",
        ["If this computer moved on purpose, press Restart watching to watch the network it is on now."], [], message) };
  }
  if (reason === 'gateway') {
    const message = 'The router and most devices stopped answering at once, so netdiag paused its checks instead of reporting every device offline.';
    return { id: 'n-33', trigger: 'netdiagLink', severity: 'warn', ip: '', title: 'The router stopped answering',
      message, at: HELD_SINCE, key, code: reason,
      advice: advice(message, ["Check that the router is switched on and its lights look normal.", "Check this computer's network cable or Wi-Fi."], [], message) };
  }
  const message = 'This computer lost the local network, so netdiag paused its checks. They start again by themselves when the network is back.';
  return { id: 'n-30', trigger: 'netdiagLink', severity: 'warn', ip: '', title: 'This computer lost the network',
    message, at: HELD_SINCE, key, code: reason,
    advice: advice("This computer lost the local network, so netdiag paused its checks instead of reporting every device offline.",
      ["Check this computer's network cable or Wi-Fi."], [], message) };
}
export function linkBackNotice() {
  return { id: 'n-31', trigger: 'netdiagLink', severity: 'info', ip: '', title: 'netdiag is back on the network',
    message: 'netdiag checks the local network again.', at: new Date().toISOString(),
    key: 'netdiagLink||' + NETWORK, ok: true, openedAt: HELD_SINCE };
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
    webServer: { url: '', mustContain: '', expectStatus: 200, trustedCert: '' },
    internetCheckURL: '',
    alerts: {
      sound: false, desktop: false, cooldownSec: 300, supplyThresholdPct: 15,
      triggers: { printerOffline: true, deviceOffline: true, supplyLow: true, printerAlert: true,
        newDevice: true, ipConflict: true, healthFailed: true },
    },
    noInternetNetworks: [],
    ignoredConflictMACs: [],
    mcp: { enabled: false },
    chat: { enabled: false, minSeverity: 'warning' },
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

// framesWith(list, conflicts, notices): a finished scan of list with these
// conflicts and notices (the advice specs).
export function framesWith(list, conflicts = [], notices = []) {
  return [
    ...list.map((h) => ({ type: 'host', data: h })),
    ...conflicts.map((c) => ({ type: 'conflict', data: { conflict: c, resolved: false } })),
    ...notices.map((n) => ({ type: 'notice', data: n })),
    { type: 'done', data: { ...scanResult({ list }), conflicts, notices } },
  ];
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

// When the fixture live scan began watching (GET /api/scans/running, 409 body).
export const LIVE_SINCE = '2026-10-10T09:30:12Z';

// GET /api/scans/running while the fixture live scan watches the network.
export function runningScan(id = SCAN_ID) {
  return { id, state: 'live', startedAt: T0, liveSince: LIVE_SINCE,
    request: { cidr: CIDR, interface: 'Wi-Fi', live: true, liveIntervalSec: 15 } };
}

// The "watching" object of the 409 POST /api/scans answers while it watches.
export function watching(id = SCAN_ID) {
  return { id, cidr: CIDR, interface: 'Wi-Fi', since: LIVE_SINCE };
}

// GET /api/identity; n picks the instance (2 = netdiag was restarted).
export function identity(n = 1) {
  return { app: 'netdiag', version: 'test', pid: 4242 + n, instanceId: 'i-test-' + n, startedAt: T0 };
}

// What a page that attaches to the running live scan receives: Bus.Subscribe's
// replay (stage, initial result, every host, the latest live status and
// health, then current conflicts and the last notices, both flagged replay).
// Presence events are never replayed.
export function replayFrames() {
  const live = liveFrames();
  const byType = (t) => live.filter((f) => f.type === t);
  const snap = byType('snapshot')[0];
  const hostsNow = new Map(snap.data.hosts.map((h) => [h.ip, h]));
  for (const f of byType('host')) hostsNow.set(f.data.ip, f.data);
  return [
    { type: 'stage', data: { stage: 'live' } },
    snap,
    ...[...hostsNow.values()].map((h) => ({ type: 'host', data: h })),
    byType('live').at(-1),
    { type: 'health', data: health() },
    ...byType('conflict').map((f) => ({ type: 'conflict', data: { ...f.data, replay: true } })),
    { type: 'notice', data: { ...notice(1), replay: true } },
    ...byType('notice').map((f) => ({ type: 'notice', data: { ...f.data, replay: true } })),
  ];
}

// ---- team chat (bubbles), v0.6 M10 ----
export const CHAT_SERVER = { name: 'Office', host: '192.168.2.20', port: 28225, onboardPort: 28226 };
export const CHAT_CHANNEL = { id: 'C1', name: 'netdiag-alerts' };
// The CA fingerprint the fake chat server hands out, in the 16 groups bubbles shows.
export const CHAT_GROUPS = ['3F2A', '9C10', '77B4', 'E0D1', '5D41', '402A', 'BC4B', '2A76', 'B971', '9D91', '1017', 'C592', '7D79', '3FCB', '0AB1', 'E6B2'];
export const CHAT_FP = 'sha256:' + CHAT_GROUPS.join('').toLowerCase();
export const CHAT_SINCE = '2026-10-10T08:40:00Z';
// The run of netdiag the status's activity note ids count in (they start again with a new one).
export const CHAT_RUN = '5f0c2a9d41e6b873';
// The status's unsaved words (chat.unsavedMsg): no OS error, no path.
export const CHAT_UNSAVED = "netdiag couldn't save the team chat connection in its data folder. It works until netdiag closes, and netdiag keeps trying to save it. Check that the data folder isn't read-only or full.";

// GET /api/bubbles in each state the status tool shows. extra adds or
// overrides fields: reason (needs_reconnect: refused or unreadable;
// no_channel: not_allowed or archived, which keep the channel) or unsaved.
export function bubblesStatus(state = 'off', extra = {}) {
  if (state === 'off') return { state: 'off', waiting: 0, dropped: 0, activity: [], run: CHAT_RUN };
  const st = { state, server: { ...CHAT_SERVER }, channel: { ...CHAT_CHANNEL }, appName: 'netdiag', tokenPrefix: 'bub_0001',
    scopes: ['alerts:write', 'channels:read', 'messages:write', 'files:write'], waiting: 0, dropped: 0, activity: [], run: CHAT_RUN };
  if (state === 'no_channel' && !extra.reason) delete st.channel;
  if (state === 'unreachable') { st.since = CHAT_SINCE; st.lastError = "netdiag can't reach the chat server."; st.waiting = 3; }
  if (state === 'needs_reconnect') { st.reason = 'refused'; st.lastError = 'The chat server no longer accepts netdiag. Click Reconnect.'; }
  if (state === 'identity_changed') { st.since = CHAT_SINCE; st.lastError = "Warning: the chat server's identity changed."; }
  return { ...st, ...extra };
}

// POST /api/bubbles/discover: the chat servers mDNS found, and the one the bubbles tray uses.
export function discover({ servers = 1, tray = false } = {}) {
  const list = [{ name: 'Office', address: CHAT_SERVER.host, port: 28225, onboardPort: 28226, fpPrefix: CHAT_FP.slice(7, 23) },
    { name: 'Warehouse', address: '192.168.2.21', port: 28225, onboardPort: 28226, fpPrefix: '0000000000000000' }].slice(0, servers);
  return { servers: list, trayServer: tray ? { name: 'Office', address: CHAT_SERVER.host, port: 28225, onboardPort: 28226 } : null };
}

// POST /api/bubbles/inspect for an address.
export function inspection(address = CHAT_SERVER.host + ':28225') {
  return { address, fingerprint: CHAT_FP, groups: [...CHAT_GROUPS], serverName: 'Office', thisComputer: SELF_IP, fpPrefixMatches: true };
}

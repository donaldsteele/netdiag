// Route mocks for /api/* plus static serving of web/ through Playwright page.route.
// Every request the page makes is either served here or recorded as unmatched/external.
import fs from 'node:fs/promises';
import path from 'node:path';
import { BASE_URL, WEB_DIR } from './config.mjs';
import * as fx from '../fixtures/index.mjs';

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.woff2': 'font/woff2',
  '.svg': 'image/svg+xml',
  '.txt': 'text/plain; charset=utf-8',
  '.json': 'application/json',
};

// Sets the storage keys the app reads at startup, before any page script runs.
export async function initStorage(page, { theme = 'dark', live = false } = {}) {
  await page.addInitScript(({ theme, live }) => {
    try {
      localStorage.setItem('netdiag.theme', theme);
      localStorage.setItem('netdiag.live', live ? '1' : '0');
    } catch (e) { /* storage blocked */ }
  }, { theme, live });
}

const matchId = (re, p) => {
  const m = p.match(re);
  return m ? decodeURIComponent(m[1]) : null;
};

// scenario: 'scan' (finite scan run ending in done) or 'live' (snapshot + live transitions).
// settings replaces the fixture settings document; frames replaces the scan's event stream;
// web: state of the web server health check served by /api/health (see fx.webCheck).
// Any web state but 'unset' sets the web server URL (unless settings already
// has one); once Settings trust a certificate, the check reports 'ok'. Like
// the server, the check reports the URL in Settings with any password
// hidden; webURL makes it report another one instead (an out-of-date result).
// repaired: JSON paths GET /api/settings lists as reset on load (F7), such as
// ['alerts.supplyThresholdPct']; like the server, a successful PUT clears it.
// health: 'default', or a fx.health state ('all': every check has advice;
// 'offline': a local network without internet, which follows the "This
// network has no internet" switch in settings for fx.NETWORK, v0.6-D15).
// osOpen: the status POST /api/os/open answers for the known page
// (204, or 501 as on macOS); any other page gets 400, as on the server.
// running: true makes GET /api/scans/running report a live scan (fx.runningScan),
// so the page reattaches on load, and POST /api/scans without replaceLive
// answers 409 like handleStart. identity: 'ok', 'down' (no answer) or
// 'restarted' (another instanceId) for the heartbeat. Specs may change
// rec.running, rec.identity and rec.frames (the event stream) while the page runs.
export async function installMocks(page, { scenario = 'scan', settings: initialSettings, frames: customFrames, web = 'unset', webURL, repaired, health: healthState = 'default', osOpen = 204, running = false, identity = 'ok' } = {}) {
  const rec = { requests: [], unmatched: [], external: [], sse: [], running, identity, frames: null, identityCalls: 0 };
  const origin = new URL(BASE_URL).origin;
  const MCP_URL = 'http://127.0.0.1:8787/mcp';
  let settings = { ...(initialSettings || fx.settings()), mcpUrl: MCP_URL };
  if (repaired && repaired.length) settings.repaired = [...repaired];
  if (web !== 'unset' && !(settings.webServer && settings.webServer.url)) settings.webServer = { ...settings.webServer, url: fx.WEB_URL };
  let history = fx.history();
  // Mirrors model.RedactURL: user:password@ becomes user:xxxxx@.
  const redact = (u) => String(u || '').replace(/^([a-z][a-z0-9+.-]*:\/\/[^/?#@:]*):[^/?#]*@/i, '$1:xxxxx@');
  const health = () => {
    if (healthState !== 'default') {
      return fx.health({ state: healthState, noInternet: (settings.noInternetNetworks || []).includes(fx.NETWORK) });
    }
    const ws = settings.webServer || {};
    return fx.health({ web: ws.trustedCert ? 'ok' : web, webURL: webURL || redact(ws.url) });
  };

  const json = (route, status, body) =>
    route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  const empty = (route, status = 204) => route.fulfill({ status, body: '' });

  async function api(route, req, url) {
    const method = req.method();
    const p = url.pathname;
    const ctype = req.headers()['content-type'] || '';
    const body = req.postData();
    if (method !== 'GET') rec.requests.push({ method, path: p, contentType: ctype, body });
    // Mirrors guard.go: every POST/PUT must declare application/json.
    if ((method === 'POST' || method === 'PUT') && ctype.split(';')[0].trim() !== 'application/json') {
      return json(route, 415, { error: 'Content-Type must be application/json' });
    }

    if (method === 'GET' && p === '/api/interfaces') return json(route, 200, fx.interfaces());
    if (method === 'GET' && p === '/api/settings') return json(route, 200, settings);
    if (method === 'PUT' && p === '/api/settings') {
      // Mirrors handlePutSettings: the saved file is valid, so the reply
      // has no repaired list (the page may send the one it got back).
      const { repaired: _sent, ...doc } = JSON.parse(body);
      settings = { ...doc, mcpUrl: MCP_URL };
      return json(route, 200, settings);
    }
    if (method === 'GET' && p === '/api/health') return json(route, 200, health());
    if (method === 'POST' && p === '/api/health/run') return json(route, 200, health());
    if (method === 'GET' && p === '/api/history') return json(route, 200, history);
    if (method === 'GET' && p === '/api/compare') {
      const base = url.searchParams.get('base') || fx.BASE_ID;
      const current = url.searchParams.get('current') || fx.SCAN_ID;
      return json(route, 200, fx.compare(base, current));
    }
    if (method === 'GET' && p === '/api/compare/export') {
      return route.fulfill({ status: 200, contentType: 'text/plain', body: 'exported' });
    }
    if (method === 'POST' && p === '/api/scans') {
      // Mirrors Registry.Start: a scan that is watching is replaced only with replaceLive.
      if (rec.running && !JSON.parse(body || '{}').replaceLive) {
        return json(route, 409, { error: 'netdiag is watching this network.', watching: fx.watching() });
      }
      return json(route, 202, { id: fx.SCAN_ID });
    }
    if (method === 'GET' && p === '/api/scans/running') {
      return json(route, 200, { scan: rec.running ? fx.runningScan() : null });
    }
    if (method === 'GET' && p === '/api/identity') {
      rec.identityCalls++;
      if (rec.identity === 'down') return route.abort('connectionrefused');
      return json(route, 200, fx.identity(rec.identity === 'restarted' ? 2 : 1));
    }
    if (method === 'POST' && p === '/api/conflicts/ignore') {
      // Mirrors Service.IgnoreMAC: the MAC is added (upper case, once) to the settings list.
      const m = String((JSON.parse(body || '{}').mac) || '').toUpperCase();
      const list = settings.ignoredConflictMACs || [];
      if (m && !list.includes(m)) settings = { ...settings, ignoredConflictMACs: [...list, m] };
      return empty(route);
    }
    if (method === 'GET' && p === '/api/remote') return json(route, 200, fx.remoteCaps());
    if (method === 'POST' && /^\/api\/scans\/[^/]+\/remote$/.test(p)) return empty(route);
    if (method === 'GET' && /^\/api\/scans\/[^/]+\/remote\.rdp$/.test(p)) {
      return route.fulfill({ status: 200, contentType: 'application/x-rdp', body: 'full address:s:' + url.searchParams.get('ip') + '\r\n' });
    }
    if (method === 'POST' && p === '/api/os/open') {
      // Mirrors handleOSOpen: only the fixed list (location) is opened.
      const pg = (JSON.parse(body || '{}') || {}).page;
      if (pg !== 'location') return json(route, 400, { error: 'unknown settings page' });
      return osOpen === 204 ? empty(route) : json(route, osOpen, { error: "netdiag can't open this settings page on this computer" });
    }
    if (method === 'POST' && p === '/api/quit') return empty(route);
    if (method === 'POST' && /^\/api\/scans\/[^/]+\/cancel$/.test(p)) return empty(route);

    const scanId = matchId(/^\/api\/scans\/([^/]+)\/events$/, p);
    if (method === 'GET' && scanId) {
      rec.sse.push(scanId);
      const frames = rec.frames || customFrames || (scenario === 'live' ? fx.liveFrames() : fx.scanFrames());
      return route.fulfill({
        status: 200,
        headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' },
        body: fx.sseBody(frames),
      });
    }
    if (method === 'GET' && matchId(/^\/api\/scans\/([^/]+)$/, p)) {
      return json(route, 200, fx.scanResult({ state: 'done' }));
    }
    const histId = matchId(/^\/api\/history\/([^/]+)$/, p);
    if (method === 'GET' && histId) return json(route, 200, fx.scanResult({ id: histId }));
    if (method === 'DELETE' && histId) {
      history = history.filter((h) => h.id !== histId);
      return empty(route);
    }

    rec.unmatched.push(`${method} ${p}`);
    return json(route, 404, { error: 'mock: no route' });
  }

  async function staticFile(route, pathname) {
    if (pathname === '/favicon.ico') return empty(route, 204);
    const rel = pathname === '/' ? '/index.html' : decodeURIComponent(pathname);
    const file = path.resolve(WEB_DIR, '.' + rel);
    if (!file.startsWith(WEB_DIR + path.sep)) return route.fulfill({ status: 403, body: 'forbidden' });
    try {
      const data = await fs.readFile(file);
      const type = MIME[path.extname(file)] || 'application/octet-stream';
      return route.fulfill({ status: 200, contentType: type, body: data });
    } catch {
      rec.unmatched.push(`GET ${pathname}`);
      return route.fulfill({ status: 404, body: 'not found' });
    }
  }

  await page.route('**/*', async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    if (url.origin !== origin) {
      rec.external.push(req.url());
      return route.abort();
    }
    if (url.pathname.startsWith('/api/')) return api(route, req, url);
    return staticFile(route, url.pathname);
  });
  return rec;
}

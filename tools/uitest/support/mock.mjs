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

// The session token the mocked netdiag launched with. Specs open the page
// at HOME, as netdiag opens the browser (/#s=<token>, v0.6 4.1).
export const SESSION = 'test-session';
export const HOME = '/#s=' + SESSION;

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
// rec.running, rec.identity and rec.frames (the event stream, or a function
// of the scan id that returns it) while the page runs, and rec.scanId: the id
// POST /api/scans answers and GET /api/scans/running reports (fx.SCAN_ID).
// session: the token the mocked netdiag expects in X-Netdiag-Session on
// every POST, PUT and DELETE and on GET /api/settings, like sessionGuard; a
// request without it gets 401 and is listed in rec.unauthorized and, unless
// stale is set (a spec that opens the page with an old token on purpose),
// in rec.unmatched too, so a spec that checks unmatched fails loudly.
// chat: the team chat state GET /api/bubbles starts in ('off', 'connected',
// 'no_channel', 'unreachable', 'needs_reconnect' or 'identity_changed',
// fx.bubblesStatus; chatExtra adds fields such as reason). found: fx.discover's options for POST
// /api/bubbles/discover. pairStates: what GET /api/bubbles/pair/status
// answers on each poll after POST /api/bubbles/pair, the last one repeated;
// 'approved' connects, with the one channel picked when channels has one.
// channels: the channels netdiag's bot is in. pairError: the error an
// approved pairing carries (the channels could not be listed, or the
// connection could not be saved). testResult: the POST
// /api/bubbles/test answer (sent alert by default). Specs may change
// rec.chat, rec.channels, rec.pairStates, rec.testResult and rec.retryTo
// (what POST /api/bubbles/retry, Try now, finds) while the page runs;
// rec.chatCalls lists the team chat actions in order.
// problems: GET /api/problems (the Problems right now card). By default it
// follows the mocked health report (fx.healthProblems, while the "Health
// check fails" alert is on) plus, once a scan was started or one is
// running, what the fixture scan has wrong (fx.problemItems); 'fine' leaves
// the scan's items out, 'some' always has them, 'fixed' adds fx.fixedItems.
// An object (or a function returning one) is served as it is. Specs may
// change rec.problems while the page runs; rec.problemsCalls counts reads
// and rec.problemsAt holds when each came (Date.now()), as rec.sseAt does
// for each event stream served.
export async function installMocks(page, { scenario = 'scan', settings: initialSettings, frames: customFrames, web = 'unset', webURL, repaired, health: healthState = 'default', osOpen = 204, running = false, identity = 'ok', session = SESSION, stale = false,
  chat = 'off', chatExtra = {}, found = {}, pairStates = ['pending', 'approved'], channels = [fx.CHAT_CHANNEL], pairError = '', testResult = null, problems } = {}) {
  const rec = { requests: [], unmatched: [], unauthorized: [], external: [], sse: [], running, identity, frames: null, identityCalls: 0, runningCalls: 0, scanId: fx.SCAN_ID, session,
    chat: fx.bubblesStatus(chat, chatExtra), pairError, channels: [...channels], pairStates: [...pairStates], pairPolls: -1, testResult, chatCalls: [], chatReads: 0,
    problems, problemsCalls: 0, problemsAt: [], sseAt: [], scanned: false };
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

  // Mirrors Service.Problems over the mocked health report and scan.
  const problemsNow = () => {
    const p = rec.problems;
    if (typeof p === 'function') return p();
    if (p && typeof p === 'object') return p;
    const h = health();
    const open = ((settings.alerts || {}).triggers || {}).healthFailed === false ? [] : fx.healthProblems(h);
    if (p === 'some' || (p !== 'fine' && (rec.scanned || rec.running))) open.push(...fx.problemItems());
    return fx.problemsView({ open, fixed: p === 'fixed' ? fx.fixedItems() : [],
      source: rec.running ? 'live' : 'latest', watching: !!rec.running, hasHealth: (h.checks || []).length > 0 });
  };

  const json = (route, status, body) =>
    route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  const empty = (route, status = 204) => route.fulfill({ status, body: '' });

  async function api(route, req, url) {
    const method = req.method();
    const p = url.pathname;
    const ctype = req.headers()['content-type'] || '';
    const body = req.postData();
    const token = req.headers()['x-netdiag-session'] || '';
    if (method !== 'GET') rec.requests.push({ method, path: p, contentType: ctype, body, session: token });
    // Mirrors guard.go: every POST/PUT must declare application/json.
    if ((method === 'POST' || method === 'PUT') && ctype.split(';')[0].trim() !== 'application/json') {
      return json(route, 415, { error: 'Content-Type must be application/json' });
    }
    // Mirrors sessionGuard: actions and the reads in sessionReads (the
    // settings, the team chat status and pairing state) need this launch's token.
    const gatedRead = p === '/api/settings' || p === '/api/bubbles' || p === '/api/bubbles/pair/status';
    if ((method !== 'GET' || gatedRead) && token !== rec.session) {
      const what = `${method} ${p} (X-Netdiag-Session: ${token ? 'wrong' : 'missing'})`;
      rec.unauthorized.push(what);
      if (!stale) rec.unmatched.push(what);
      return json(route, 401, { error: 'Open netdiag again to use this button.', session: false });
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
    if (method === 'GET' && p === '/api/problems') { rec.problemsCalls++; rec.problemsAt.push(Date.now()); return json(route, 200, problemsNow()); }
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
        return json(route, 409, { error: 'netdiag is watching this network.', watching: fx.watching(rec.scanId) });
      }
      rec.scanned = true;
      return json(route, 202, { id: rec.scanId });
    }
    if (method === 'GET' && p === '/api/scans/running') {
      rec.runningCalls++;
      return json(route, 200, { scan: rec.running ? fx.runningScan(rec.scanId) : null });
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
    if (p === '/api/bubbles' || p.startsWith('/api/bubbles/')) return chatRoute(route, method, p, JSON.parse(body || '{}'));
    if (method === 'POST' && p === '/api/quit') return empty(route);
    if (method === 'POST' && /^\/api\/scans\/[^/]+\/cancel$/.test(p)) return empty(route);

    const scanId = matchId(/^\/api\/scans\/([^/]+)\/events$/, p);
    if (method === 'GET' && scanId) {
      rec.sse.push(scanId); rec.sseAt.push(Date.now());
      const set = typeof rec.frames === 'function' ? rec.frames(scanId) : rec.frames;
      const frames = set || customFrames || (scenario === 'live' ? fx.liveFrames() : fx.scanFrames());
      return route.fulfill({
        status: 200,
        headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' },
        body: fx.sseBody(frames),
      });
    }
    if (method === 'GET' && /^\/api\/scans\/[^/]+\/export$/.test(p)) {
      return route.fulfill({ status: 200, contentType: 'text/plain', body: 'exported' });
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

  // Mirrors internal/server/handlers_chat.go and chat.Manager for the wizard,
  // the Settings section and the header status tool.
  const LOCAL = /^(10\.|127\.|192\.168\.|169\.254\.|172\.(1[6-9]|2\d|3[01])\.|100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.)|^[a-z0-9.-]+\.local(:|$)/i;
  const chatChoice = () => ({ state: rec.chat.state, channel: rec.chat.channel || undefined, channels: rec.channels });
  const connect = () => {
    rec.chat = fx.bubblesStatus('connected');
    if (rec.channels.length === 1) rec.chat.channel = { ...rec.channels[0] };
    else { delete rec.chat.channel; rec.chat.state = 'no_channel'; }
  };
  async function chatRoute(route, method, p, b) {
    if (method !== 'GET') rec.chatCalls.push(method + ' ' + p);
    if (method === 'GET' && p === '/api/bubbles') { rec.chatReads++; return json(route, 200, rec.chat); }
    if (method === 'DELETE' && p === '/api/bubbles') { rec.chat = fx.bubblesStatus('off'); return empty(route); }
    if (method === 'POST' && p === '/api/bubbles/discover') return json(route, 200, fx.discover(found));
    if (method === 'POST' && p === '/api/bubbles/inspect') {
      if (!LOCAL.test(String(b.address || ''))) {
        return json(route, 400, { error: 'Use the address of a chat server on the local network, for example 192.168.1.20 or office.local.' });
      }
      const a = String(b.address).includes(':') ? b.address : b.address + ':28225';
      return json(route, 200, fx.inspection(a));
    }
    if (method === 'POST' && p === '/api/bubbles/pair') {
      if (!b.confirmed && !b.reconnect) return json(route, 400, { error: "Compare the chat server's fingerprint first." });
      rec.pairPolls = 0;
      return json(route, 200, { code: '123 456', expiresAt: new Date(Date.now() + 10 * 60000).toISOString(),
        adminUrl: 'https://' + fx.CHAT_SERVER.host + ':28225/admin/pairing' });
    }
    if (method === 'GET' && p === '/api/bubbles/pair/status') {
      if (rec.pairPolls < 0) return json(route, 200, { state: 'none', channels: [] });
      const state = rec.pairStates[Math.min(rec.pairPolls, rec.pairStates.length - 1)];
      rec.pairPolls++;
      const words = { denied: 'Your chat admin said no.', expired: 'The request ran out of time after 10 minutes.' };
      if (state === 'approved') {
        connect();
        if (rec.pairError) rec.chat.unsaved = rec.pairError;
        return json(route, 200, { ...chatChoice(), state, code: '123 456', error: rec.pairError || undefined });
      }
      return json(route, 200, { state, code: '123 456', channels: [], error: words[state] });
    }
    if (method === 'DELETE' && p === '/api/bubbles/pair') { rec.pairPolls = -1; return empty(route); }
    if (method === 'POST' && p === '/api/bubbles/connect') {
      if (!String(b.connectionString || '').startsWith('bubbles://')) {
        return json(route, 400, { error: 'This is not a team chat connection string. It starts with bubbles://' });
      }
      connect();
      return json(route, 200, chatChoice());
    }
    if (method === 'POST' && p === '/api/bubbles/channel') {
      if (rec.chat.state === 'off') return json(route, 400, { error: "netdiag isn't connected to team chat. Click Connect to team chat." });
      // Like SetChannel: the chosen channel, the only one, or the current one
      // when the bot is in it; a chosen channel takes posts again and is saved.
      const cur = (rec.chat.channel || {}).id;
      const pick = b.channelId ? rec.channels.find((c) => c.id === b.channelId)
        : rec.channels.length === 1 ? rec.channels[0] : rec.channels.find((c) => c.id === cur) || null;
      if (b.channelId && !pick) return json(route, 400, { error: "netdiag isn't in that channel. Choose another one, or ask your chat admin to add netdiag to it." });
      if (pick) {
        rec.chat.channel = { ...pick };
        if (rec.chat.state === 'no_channel') rec.chat.state = 'connected';
        if (rec.chat.state === 'connected') delete rec.chat.reason;
        delete rec.chat.unsaved;
        rec.pairError = '';
      }
      return json(route, 200, chatChoice());
    }
    if (method === 'POST' && p === '/api/bubbles/test') {
      return json(route, 200, rec.testResult || { ok: true, transition: 'created', channel: (rec.chat.channel || {}).name });
    }
    if (method === 'POST' && p === '/api/bubbles/retry') {
      // Try now answers the status; rec.retryTo is the state the server is
      // found in (unchanged when unset).
      if (rec.retryTo) rec.chat = fx.bubblesStatus(rec.retryTo, rec.retryExtra || {});
      return json(route, 200, rec.chat);
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

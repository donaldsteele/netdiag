// F9a "Never lose netdiag": a reloaded page attaches to the scan that is
// watching (silently, a fixed alert as Fixed), Restart watching asks first, a
// stale tab cannot stop the watching, the heartbeat notices when netdiag
// stops or restarts, every tab follows the scan that is watching, and only
// one of the tabs following it beeps.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME } from '../support/mock.mjs';
import * as fx from '../fixtures/index.mjs';
import { runAxe } from '../support/axe.mjs';
import { THEMES } from '../support/config.mjs';

// Counts the oscillators beep() starts; reset on every page load.
async function spyBeeps(page) {
  await page.addInitScript(() => {
    window.__beeps = 0;
    const Real = window.AudioContext || window.webkitAudioContext;
    if (!Real) return;
    window.AudioContext = class extends Real {
      createOscillator() { window.__beeps++; return super.createOscillator(); }
    };
  });
}
const beeps = (page) => page.evaluate(() => window.__beeps);

function soundOn() {
  const s = fx.settings();
  s.alerts.sound = true;
  return s;
}

// Sound and desktop alerts on.
function loudSettings() {
  const s = soundOn();
  s.alerts.desktop = true;
  return s;
}

// Records desktop notifications, with permission granted.
async function spyNotes(page) {
  await page.addInitScript(() => {
    window.__notes = [];
    window.Notification = class {
      constructor(title, opts) { window.__notes.push({ title, ...opts }); }
      static get permission() { return 'granted'; }
      static requestPermission() { return Promise.resolve('granted'); }
    };
  });
}
const notes = (page) => page.evaluate(() => window.__notes);

// The browser logs every 409 answer; the stale tab test expects one.
function watchErrors(page) {
  const errors = [];
  page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`));
  page.on('console', (m) => {
    if (m.type() === 'error' && !/favicon|status of 409/i.test(m.text())) errors.push(`console: ${m.text()}`);
  });
  return errors;
}

// The page's Alpine component, for checks the DOM does not show.
const app = (page, fn, arg) => page.evaluate(([src, a]) => {
  const el = document.querySelector('.shell');
  const data = el && el._x_dataStack ? el._x_dataStack[0] : null;
  if (!data) return null; // the page is still loading
  return new Function('app', 'arg', 'return (' + src + ')(app, arg)')(data, a);
}, [fn.toString(), arg]);

const posts = (rec) => rec.requests.filter((r) => r.method === 'POST' && r.path === '/api/scans');
const restartDialog = (page) => page.locator('#restart-dialog');

test('reload while live brings back the table, health, banners and live pill with no beep', async ({ page }) => {
  const errors = watchErrors(page);
  await spyBeeps(page);
  await initStorage(page, { theme: 'dark', live: true });
  const rec = await installMocks(page, { scenario: 'live', settings: soundOn() });
  await page.goto(HOME);
  await page.getByRole('button', { name: 'Scan', exact: true }).click();
  await expect(page.locator('.livepill')).toBeVisible();
  await expect(page.locator('#alerts div.banner:not(.more)')).toContainText('Device offline');
  await expect.poll(() => beeps(page), { message: 'the live notice beeps before the reload' }).toBeGreaterThan(0);

  // The server is watching; a reload replays its state.
  rec.running = true;
  rec.frames = fx.replayFrames();
  await page.reload();

  // 254 rows can take a while on a busy machine.
  await expect(page.locator('.livepill')).toBeVisible({ timeout: 15_000 });
  await expect(page.locator('#hosts tbody tr.host')).toHaveCount(fx.TOTAL_HOSTS, { timeout: 15_000 });
  await expect(page.locator('#hosts tr.host').filter({ hasText: '192.168.2.200' }).locator('.badge.offline')).toBeVisible();
  await expect(page.locator('.hstrip .hpill')).toHaveCount(6);
  await expect(page.locator('#hp-wifi')).toHaveClass(/hs-warn/);
  const banners = page.locator('#alerts div.banner:not(.more)');
  await expect(banners).toHaveCount(2);
  await expect(banners.first()).toContainText('Device offline');
  await expect(banners.nth(1)).toContainText('Toner low');
  await expect(page.getByRole('button', { name: 'Stop live', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Restart watching', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Scan', exact: true })).toBeHidden();
  await expect(page.getByLabel('Target (CIDR or range)')).toHaveValue(fx.CIDR);
  await expect(page.locator('#live-mode')).toBeChecked();

  // Silent: no sound, no notice lines in Activity, and no new scan.
  await page.waitForTimeout(500);
  expect(await beeps(page), 'beeps after the reload').toBe(0);
  expect(await app(page, (a) => a.log.filter((e) => e.kind === 'notice').length)).toBe(0);
  expect(await app(page, (a) => a.log.some((e) => e.text.startsWith('Reconnected to the running scan')))).toBe(true);
  expect(posts(rec), 'POST /api/scans').toHaveLength(1);
  expect(rec.sse).toEqual([fx.SCAN_ID, fx.SCAN_ID]);

  expect(errors, 'page errors').toEqual([]);
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
  expect(rec.external, 'external requests').toEqual([]);
});

test('a stale tab cannot stop the watching: Scan asks, Keep watching attaches', async ({ page }) => {
  const errors = watchErrors(page);
  await initStorage(page, { theme: 'dark', live: true });
  const rec = await installMocks(page, { scenario: 'live' });
  await page.goto(HOME);
  await expect(page.getByRole('button', { name: 'Scan', exact: true })).toBeVisible();
  expect(rec.sse).toEqual([]);
  // The page looked for a running scan on load and after the first
  // heartbeat; the next look is 15 s away.
  await expect.poll(() => rec.runningCalls).toBeGreaterThanOrEqual(2);
  await page.waitForTimeout(200);

  // Another tab (or a second launch) started watching after this page last
  // looked.
  rec.running = true;
  rec.frames = fx.replayFrames();
  await page.getByRole('button', { name: 'Scan', exact: true }).click();
  const dlg = restartDialog(page);
  await expect(dlg).toHaveJSProperty('open', true);
  const since = await page.evaluate((t) => new Date(t).toTimeString().slice(0, 5), fx.LIVE_SINCE);
  await expect(dlg.locator('#restart-q')).toHaveText(`netdiag is watching this network (since ${since}). Restart with these settings?`);
  await expect(dlg.locator('#restart-keep')).toBeFocused();
  expect(posts(rec)).toHaveLength(1);
  expect(JSON.parse(posts(rec)[0].body).replaceLive).toBeUndefined();

  await dlg.getByRole('button', { name: 'Keep watching', exact: true }).click();
  await expect(dlg).toHaveJSProperty('open', false);
  await expect(page.locator('.livepill')).toBeVisible();
  await expect(page.locator('#hosts tbody tr.host')).toHaveCount(fx.TOTAL_HOSTS);
  await expect(page.locator('#restart-btn')).toBeFocused();
  expect(posts(rec), 'Keep watching sends nothing').toHaveLength(1);
  expect(rec.sse).toEqual([fx.SCAN_ID]);

  expect(errors, 'page errors').toEqual([]);
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

test('Restart watching asks first; Esc keeps watching and returns focus; Restart replaces', async ({ page }) => {
  const errors = watchErrors(page);
  await initStorage(page, { theme: 'dark', live: true });
  const rec = await installMocks(page, { scenario: 'live', running: true, frames: fx.replayFrames() });
  await page.goto(HOME);
  const btn = page.getByRole('button', { name: 'Restart watching', exact: true });
  await expect(btn).toBeVisible();
  await expect(page.locator('#hosts tbody tr.host')).toHaveCount(fx.TOTAL_HOSTS);

  await btn.click();
  const dlg = restartDialog(page);
  await expect(dlg).toHaveJSProperty('open', true);
  await expect(dlg.locator('#restart-keep')).toBeFocused();
  await expect(dlg.locator('#restart-settings')).toHaveText(`These settings: ${fx.CIDR}, live mode.`);
  await page.keyboard.press('Escape');
  await expect(dlg).toHaveJSProperty('open', false);
  await expect(btn).toBeFocused();
  await expect(page.locator('.livepill')).toBeVisible();
  expect(posts(rec), 'Esc sends nothing').toHaveLength(0);

  await btn.click();
  await expect(dlg).toHaveJSProperty('open', true);
  await dlg.getByRole('button', { name: 'Restart', exact: true }).click();
  await expect(dlg).toHaveJSProperty('open', false);
  await expect.poll(() => posts(rec).length).toBe(1);
  expect(JSON.parse(posts(rec)[0].body)).toMatchObject({ cidr: fx.CIDR, live: true, replaceLive: true });
  await expect(page.locator('.livepill')).toBeVisible();
  expect(await app(page, (a) => a.log.some((e) => e.text.startsWith('Watching restarted: ')))).toBe(true);

  expect(errors, 'page errors').toEqual([]);
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

for (const theme of THEMES) {
  test(`restart dialog and netdiag banners are accessible ${theme}`, async ({ page }, testInfo) => {
    await initStorage(page, { theme, live: true });
    const rec = await installMocks(page, { scenario: 'live', running: true, frames: fx.replayFrames() });
    await page.goto(HOME);
    await page.getByRole('button', { name: 'Restart watching', exact: true }).click();
    await expect(restartDialog(page)).toHaveJSProperty('open', true);
    await runAxe(page, `restart-dialog-${theme}`, testInfo);
    await page.keyboard.press('Escape');
    await app(page, (a) => { a.backend = 'restarted'; });
    await expect(page.locator('#backend-restarted')).toBeVisible();
    await app(page, (a) => { a.backend = 'stopped'; });
    await expect(page.locator('#backend-stopped')).toBeVisible();
    await runAxe(page, `backend-stopped-${theme}`, testInfo);
    expect(rec.unmatched, 'unmatched requests').toEqual([]);
  });
}

test('the stopped banner appears within 30 s when netdiag stops, and goes when it answers again', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.goto(HOME);
  await expect.poll(() => rec.identityCalls).toBeGreaterThan(0);
  const stopped = page.locator('#backend-stopped');
  await expect(stopped).toBeHidden();

  rec.identity = 'down';
  const t0 = Date.now();
  await expect(stopped).toBeVisible({ timeout: 30_000 });
  expect(Date.now() - t0, 'ms until the stopped banner').toBeLessThan(30_000);
  await expect(stopped).toHaveText('netdiag has stopped. Open netdiag again to keep watching.');
  await expect(stopped).toHaveAttribute('role', 'status');

  rec.identity = 'ok';
  await expect(stopped).toBeHidden({ timeout: 15_000 });
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

test('while live, a broken event stream checks every 5 s, so the stopped banner comes sooner', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: true });
  const rec = await installMocks(page, { scenario: 'live', running: true, frames: fx.replayFrames() });
  await page.goto(HOME);
  await expect(page.locator('.livepill')).toBeVisible();
  // The mock stream ends after the replay, like a backend that went away.
  // The replay draws every host first, which takes a while on a busy machine.
  await expect.poll(() => app(page, (a) => a.sseErr), { timeout: 20_000 }).toBe(true);
  rec.identity = 'down';
  const t0 = Date.now();
  await expect(page.locator('#backend-stopped')).toBeVisible({ timeout: 30_000 });
  expect(Date.now() - t0, 'ms until the stopped banner').toBeLessThan(15_000);
  // The live view stays on screen.
  await expect(page.locator('#hosts tbody tr.host')).toHaveCount(fx.TOTAL_HOSTS);
});

test('a restarted netdiag shows the restarted banner with Reload page', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.goto(HOME);
  await expect.poll(() => rec.identityCalls).toBeGreaterThan(0);
  rec.identity = 'restarted';
  const banner = page.locator('#backend-restarted');
  await expect(banner).toBeVisible({ timeout: 30_000 });
  await expect(banner).toContainText('netdiag was restarted.');
  await expect(page.locator('#backend-stopped')).toBeHidden();
  await banner.getByRole('button', { name: 'Reload page', exact: true }).click();
  // The new page belongs to the new netdiag.
  await expect.poll(() => app(page, (a) => a.instanceId)).toBe('i-test-2');
  await expect(page.locator('#backend-restarted')).toBeHidden();
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

test('quitting stops the heartbeat and shows only the quit message', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.goto(HOME);
  await expect.poll(() => rec.identityCalls).toBeGreaterThan(0);
  await page.getByRole('button', { name: 'Quit', exact: true }).click();
  await expect(page.getByText('netdiag has stopped. You can close this tab.')).toBeVisible();
  rec.identity = 'down';
  await app(page, (a) => a.hbTick());
  await app(page, (a) => a.hbTick());
  await expect(page.locator('#backend-stopped')).toBeHidden();
});

// Alpine calls init() itself. Calling it again from x-init opened a second
// tab channel and a second heartbeat, so one slow moment counted as both of
// the failures that mean netdiag has stopped.
test('the page starts once: one tab channel, one heartbeat, one attach', async ({ page }) => {
  await page.addInitScript(() => {
    window.__channels = 0;
    const Real = window.BroadcastChannel;
    window.BroadcastChannel = class extends Real { constructor(name) { super(name); window.__channels++; } };
  });
  let lists = 0;
  page.on('request', (r) => { if (new URL(r.url()).pathname === '/api/interfaces') lists++; });
  await initStorage(page, { theme: 'dark', live: true });
  const rec = await installMocks(page, { scenario: 'live', running: true, frames: fx.replayFrames() });
  await page.goto(HOME);
  await expect(page.locator('.livepill')).toBeVisible({ timeout: 15_000 });
  await expect.poll(() => app(page, (a) => a.settings !== null), { timeout: 15_000 }).toBe(true);
  await expect.poll(() => rec.identityCalls).toBeGreaterThan(0);
  await page.waitForTimeout(300);
  expect(await page.evaluate(() => window.__channels), 'tab channels').toBe(1);
  expect(lists, 'interface lists').toBe(1);
  expect(rec.runningCalls, 'running scan lookups').toBe(1);
  expect(rec.sse, 'event streams').toEqual([fx.SCAN_ID]);
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

// The 4 s limit is for netdiag, not for a page busy drawing a big table: a
// heartbeat timer that fires late gives the waiting answer more time.
test('a page too busy to read the heartbeat answer in time does not call netdiag stopped', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.goto(HOME);
  await expect.poll(() => rec.identityCalls).toBeGreaterThan(0);
  await expect.poll(() => app(page, (a) => a.settings !== null), { timeout: 15_000 }).toBe(true);
  // The answer comes after 300 ms, while the page is busy.
  await page.route('**/api/identity', async (route) => {
    await new Promise((r) => setTimeout(r, 300));
    await route.fallback();
  });
  for (let i = 0; i < 2; i++) {
    const calls = rec.identityCalls;
    const fails = await app(page, async (a) => {
      const beat = a.beat();
      await new Promise((r) => setTimeout(r, 50));
      const end = Date.now() + 5000;
      while (Date.now() < end) { /* the page is busy for 5 s */ }
      await beat;
      return a.hbFails;
    });
    expect(rec.identityCalls, 'the heartbeat asked').toBeGreaterThan(calls);
    expect(fails, 'heartbeat failures').toBe(0);
  }
  await expect(page.locator('#backend-stopped')).toBeHidden();
  expect(await app(page, (a) => a.backend)).toBe('ok');
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

test('a heartbeat call that netdiag never answers gives up after 4 s', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.goto(HOME);
  await expect.poll(() => rec.identityCalls).toBeGreaterThan(0);
  // No heartbeat of its own during the test; let one already asking finish.
  await app(page, (a) => a.hbSchedule(600_000));
  await page.waitForTimeout(200);
  await page.route('**/api/identity', () => {}); // netdiag hangs
  const r = await app(page, async (a) => {
    const t0 = Date.now();
    await a.beat();
    return { ms: Date.now() - t0, fails: a.hbFails };
  });
  expect(r.fails, 'heartbeat failures').toBe(1);
  expect(r.ms, 'ms until it gave up').toBeGreaterThanOrEqual(3_900);
  expect(r.ms, 'ms until it gave up').toBeLessThan(10_000);
});


// A replayed OK notice turns its fault's banner into Fixed as silently as
// the fault's banner came back: no Activity line, sound or desktop pop-up.
test('a replayed fault and its OK come back as a Fixed banner, silently', async ({ page }) => {
  const errors = watchErrors(page);
  await spyBeeps(page);
  await spyNotes(page);
  await initStorage(page, { theme: 'dark', live: true });
  const frames = [...fx.replayFrames(), { type: 'notice', data: { ...fx.fixedNotice(2), replay: true } }];
  const rec = await installMocks(page, { scenario: 'live', settings: loudSettings(), running: true, frames });
  await page.goto(HOME);
  // 254 rows can take a while on a busy machine.
  await expect(page.locator('.livepill')).toBeVisible({ timeout: 15_000 });
  const banners = page.locator('#alerts div.banner:not(.more)');
  const fixed = banners.filter({ hasText: 'Fixed: Device back online' });
  await expect(fixed).toHaveCount(1, { timeout: 15_000 });
  await expect(fixed).toHaveClass(/sev-ok/);
  await expect(fixed).toContainText('(was down 12 min).');
  // The fault's banner became the Fixed one; Toner low is still open.
  await expect(banners).toHaveCount(2);
  await expect(banners.filter({ hasText: 'Warning: Toner low' })).toHaveCount(1);
  await expect.poll(() => app(page, (a) => a.settings !== null)).toBe(true);

  await page.waitForTimeout(500);
  expect(await beeps(page), 'beeps').toBe(0);
  expect(await notes(page), 'desktop notifications').toEqual([]);
  expect(await app(page, (a) => a.log.filter((e) => e.kind === 'notice').length), 'notice and Fixed lines').toBe(0);

  // Reloading again rebuilds the same two banners, still silently.
  await page.reload();
  await expect(page.locator('.livepill')).toBeVisible({ timeout: 15_000 });
  await expect(page.locator('#alerts div.banner:not(.more)').filter({ hasText: 'Fixed: Device back online' })).toHaveCount(1, { timeout: 15_000 });
  await expect(page.locator('#alerts div.banner:not(.more)')).toHaveCount(2);
  await page.waitForTimeout(500);
  expect(await beeps(page)).toBe(0);
  expect(await notes(page)).toEqual([]);
  expect(errors, 'page errors').toEqual([]);
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

// Two tabs of one browser share the BroadcastChannel. Each has its own
// mocks, which the tests keep in step like one netdiag would.
async function twoTabs(page, opts = {}) {
  const other = await page.context().newPage();
  const recs = [];
  for (const p of [page, other]) {
    await spyBeeps(p);
    await spyNotes(p);
    await initStorage(p, { theme: 'dark', live: true });
    recs.push(await installMocks(p, { scenario: 'live', settings: loudSettings(), ...opts }));
  }
  return { other, recA: recs[0], recB: recs[1] };
}
// Attaching rebuilds 254 rows, which can take a while on a busy machine.
const SLOW = { timeout: 15_000 };
// (The live pill always holds the word Live, so the tests check that it shows.)
const settled = (p) => expect.poll(() => app(p, (a) => a.settings !== null), SLOW).toBe(true);
const notify = (p, n) => app(p, (a, x) => a.onNotice(x, a.scanId), n);
const focusTab = (p) => p.evaluate(() => window.dispatchEvent(new Event('focus')));
const NEW_ID = 'c0ffee00-0000-4000-8000-000000000002';

test('of the tabs following the scan, only the one focused last beeps', async ({ page }) => {
  const { other, recA, recB } = await twoTabs(page, { running: true, frames: fx.replayFrames() });
  await page.goto(HOME);
  await expect(page.locator('.livepill')).toBeVisible(SLOW);
  await other.goto(HOME);
  await expect(other.locator('.livepill')).toBeVisible(SLOW);
  await settled(page);
  await settled(other);

  // The tab opened last plays the sound; the other only shows the banner.
  await expect.poll(() => app(page, (a) => a.isBeeper()), SLOW).toBe(false);
  expect(await app(other, (a) => a.isBeeper())).toBe(true);
  const fault = { id: 'n-50', trigger: 'printerOffline', severity: 'warn', title: 'Printer offline',
    message: 'test', key: 'printerOffline|00:1B:A9:12:34:56|', at: new Date().toISOString() };
  await notify(page, fault);
  await notify(other, fault);
  await expect(page.locator('#alerts div.banner:not(.more)').filter({ hasText: 'Printer offline' })).toHaveCount(1);
  await expect(other.locator('#alerts div.banner:not(.more)').filter({ hasText: 'Printer offline' })).toHaveCount(1);
  expect(await beeps(page), 'first tab').toBe(0);
  expect(await beeps(other), 'last opened tab').toBeGreaterThan(0);
  expect(await notes(page)).toHaveLength(0);
  expect(await notes(other)).toHaveLength(1);

  // Focusing the first tab makes it the one that beeps.
  await focusTab(page);
  await expect.poll(() => app(other, (a) => a.isBeeper()), SLOW).toBe(false);
  expect(await app(page, (a) => a.isBeeper())).toBe(true);

  // The Fixed desktop notification comes from the beeping tab only; both
  // banners turn into Fixed.
  const ok = { ...fault, id: 'n-51', severity: 'info', title: 'Printer back online', message: 'Printer answers again.',
    ok: true, openedAt: fault.at, at: new Date().toISOString() };
  await notify(page, ok);
  await notify(other, ok);
  for (const p of [page, other]) {
    await expect(p.locator('#alerts div.banner:not(.more)').filter({ hasText: 'Fixed: Printer back online' })).toHaveCount(1);
  }
  expect((await notes(page)).map((n) => n.title)).toEqual(['Fixed: Printer back online']);
  expect(await notes(other)).toHaveLength(1);

  const before = await beeps(other);
  const next = { ...fault, id: 'n-52' };
  await notify(page, next);
  await notify(other, next);
  expect(await beeps(page), 'focused tab').toBeGreaterThan(0);
  expect(await beeps(other), 'other tab').toBe(before);

  // When the beeping tab closes, the one left beeps again.
  await page.close({ runBeforeUnload: true });
  await expect.poll(() => app(other, (a) => a.isBeeper()), SLOW).toBe(true);
  await other.close();
  expect(recA.unmatched.concat(recB.unmatched)).toEqual([]);
});

test('a tab that is not following the scan never takes the beeping, and follows the scan once it can', async ({ page }) => {
  // page: open, idle, and (for now) told no scan is running. other: watching.
  const { other, recA, recB } = await twoTabs(page);
  await page.goto(HOME);
  await expect(page.getByRole('button', { name: 'Scan', exact: true })).toBeVisible();
  await other.goto(HOME);
  await other.getByRole('button', { name: 'Scan', exact: true }).click();
  await expect(other.locator('.livepill')).toBeVisible(SLOW);
  await settled(page);
  await settled(other);
  expect(await app(other, (a) => a.isBeeper())).toBe(true);

  // The person clicks the idle tab, for example to look at History.
  await focusTab(page);
  await page.waitForTimeout(300);
  expect(await app(page, (a) => a.isBeeper()), 'idle tab').toBe(false);
  expect(await app(other, (a) => a.isBeeper()), 'watching tab').toBe(true);
  expect(await app(page, (a) => a.attached())).toBe(false);
  const fault = { id: 'n-60', trigger: 'printerOffline', severity: 'warn', title: 'Printer offline', message: 'test',
    key: 'printerOffline|00:1B:A9:12:34:56|', at: new Date().toISOString() };
  // (The live stream's Device offline notice already beeped there.)
  const tones = await beeps(other);
  await notify(other, fault);
  await expect.poll(() => beeps(other), 'the watching tab still beeps').toBeGreaterThan(tones);
  expect((await notes(other)).filter((n) => n.title === 'Printer offline')).toHaveLength(1);
  // A notice in a tab that follows no scan makes no sound either.
  await notify(page, fault);
  expect(await beeps(page)).toBe(0);

  // The heartbeat finds the live scan and the idle tab follows it. It was
  // focused last, so it beeps from then on.
  recA.running = true;
  recA.frames = fx.replayFrames();
  await app(page, (a) => a.beat());
  await expect(page.locator('.livepill')).toBeVisible(SLOW);
  await expect(page.getByRole('button', { name: 'Scan', exact: true })).toBeHidden();
  await expect.poll(() => app(page, (a) => a.isBeeper()), SLOW).toBe(true);
  await expect.poll(() => app(other, (a) => a.isBeeper()), SLOW).toBe(false);
  expect(recA.sse).toEqual([fx.SCAN_ID]);
  expect(await app(page, (a) => a.log.some((e) => e.text.startsWith('Reconnected to the running scan')))).toBe(true);
  expect(recA.unmatched.concat(recB.unmatched)).toEqual([]);
  await other.close();
});

test('a scan without live mode in another tab does not pull this tab along', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.goto(HOME);
  await expect(page.getByRole('button', { name: 'Scan', exact: true })).toBeVisible();
  await page.route('**/api/scans/running', (route) => route.fulfill({ status: 200, contentType: 'application/json',
    body: JSON.stringify({ scan: { ...fx.runningScan(), state: 'running', liveSince: undefined,
      request: { cidr: fx.CIDR, interface: 'Wi-Fi', live: false } } }) }));
  await app(page, (a) => a.beat());
  await focusTab(page);
  await page.waitForTimeout(300);
  expect(rec.sse).toEqual([]);
  await expect(page.getByRole('button', { name: 'Scan', exact: true })).toBeVisible();
});

test('Restart watching in one tab: the other tab follows the new scan', async ({ page }) => {
  const { other, recA, recB } = await twoTabs(page, { running: true, frames: fx.replayFrames() });
  await page.goto(HOME);
  await expect(page.locator('.livepill')).toBeVisible(SLOW);
  await other.goto(HOME);
  await expect(other.locator('.livepill')).toBeVisible(SLOW);
  await settled(page);
  await settled(other);

  // One netdiag: after the restart it reports the new scan to both tabs.
  recA.scanId = NEW_ID;
  recB.scanId = NEW_ID;
  await page.getByRole('button', { name: 'Restart watching', exact: true }).click();
  await page.locator('#restart-dialog').getByRole('button', { name: 'Restart', exact: true }).click();
  await expect.poll(() => recA.sse, SLOW).toEqual([fx.SCAN_ID, NEW_ID]);
  await expect.poll(() => recB.sse, { message: 'the other tab follows', ...SLOW }).toEqual([fx.SCAN_ID, NEW_ID]);
  await expect(other.locator('.livepill')).toBeVisible(SLOW);
  await expect(other.getByRole('button', { name: 'Restart watching', exact: true })).toBeVisible();
  await expect(other.getByRole('button', { name: 'Scan', exact: true })).toBeHidden();
  expect(await app(other, (a) => a.scanId)).toBe(NEW_ID);
  // The tab the person used beeps.
  await expect.poll(() => app(page, (a) => a.isBeeper()), SLOW).toBe(true);
  await expect.poll(() => app(other, (a) => a.isBeeper()), SLOW).toBe(false);
  expect(recA.unmatched.concat(recB.unmatched)).toEqual([]);
  await other.close();
});

test('when watching is restarted elsewhere, the page follows the new scan instead of showing it finished', async ({ page }) => {
  const errors = watchErrors(page);
  await initStorage(page, { theme: 'dark', live: true });
  const rec = await installMocks(page, { scenario: 'live', running: true, frames: fx.replayFrames() });
  await page.goto(HOME);
  await expect(page.locator('.livepill')).toBeVisible(SLOW);

  // The old scan's stream ends with done (cancelled): another browser, or a
  // tab that is not open here, restarted watching.
  rec.scanId = NEW_ID;
  await app(page, (a) => a.es.dispatchEvent(new MessageEvent('done', { data: JSON.stringify({ state: 'cancelled' }) })));
  await expect.poll(() => rec.sse, SLOW).toEqual([fx.SCAN_ID, NEW_ID]);
  await expect(page.locator('.livepill')).toBeVisible(SLOW);
  await expect(page.getByRole('button', { name: 'Restart watching', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Scan', exact: true })).toBeHidden();
  expect(await app(page, (a) => a.scanId)).toBe(NEW_ID);
  expect(errors, 'page errors').toEqual([]);
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

// A tab that hears another tab restart watching while it shows netdiag
// stopped cannot follow then; it looks again as soon as netdiag answers.
test('a tab told of a new scan while netdiag seemed stopped follows it once netdiag answers', async ({ page }) => {
  const errors = watchErrors(page);
  await initStorage(page, { theme: 'dark', live: true });
  const rec = await installMocks(page, { scenario: 'live', running: true, frames: fx.replayFrames() });
  await page.goto(HOME);
  await expect(page.locator('.livepill')).toBeVisible(SLOW);
  await settled(page);
  // No heartbeat of its own during the test; let one already asking finish.
  await app(page, (a) => a.hbSchedule(600_000));
  await page.waitForTimeout(200);

  await app(page, (a) => { a.backend = 'stopped'; });
  await expect(page.locator('#backend-stopped')).toBeVisible();
  rec.scanId = NEW_ID;
  await page.evaluate((id) => {
    window.__otherTab = new BroadcastChannel('netdiag');
    window.__otherTab.postMessage({ type: 'started', id });
  }, NEW_ID);
  await page.waitForTimeout(300);
  expect(rec.sse, 'no follow while netdiag seems stopped').toEqual([fx.SCAN_ID]);

  await app(page, (a) => a.beat());
  await expect.poll(() => rec.sse, SLOW).toEqual([fx.SCAN_ID, NEW_ID]);
  await expect(page.locator('#backend-stopped')).toBeHidden();
  await expect(page.locator('.livepill')).toBeVisible(SLOW);
  expect(await app(page, (a) => a.scanId)).toBe(NEW_ID);
  expect(errors, 'page errors').toEqual([]);
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

// Only the stream the tab follows can end its attach: a done from a stream
// it already left changes nothing.
test('a done from a scan the tab already left does not clear the new attach', async ({ page }) => {
  const errors = watchErrors(page);
  await initStorage(page, { theme: 'dark', live: true });
  const rec = await installMocks(page, { scenario: 'live', running: true, frames: fx.replayFrames() });
  await page.goto(HOME);
  await expect(page.locator('.livepill')).toBeVisible(SLOW);
  await settled(page);
  await app(page, (a) => { window.__oldStream = a.es; });

  rec.scanId = NEW_ID;
  await page.evaluate((id) => {
    window.__otherTab = new BroadcastChannel('netdiag');
    window.__otherTab.postMessage({ type: 'started', id });
  }, NEW_ID);
  await expect.poll(() => rec.sse, SLOW).toEqual([fx.SCAN_ID, NEW_ID]);
  await expect.poll(() => app(page, (a) => a.scanId), SLOW).toBe(NEW_ID);
  await page.evaluate(() => window.__oldStream.dispatchEvent(new MessageEvent('done', { data: JSON.stringify({ state: 'cancelled' }) })));
  await page.waitForTimeout(300);
  expect(await app(page, (a) => a.attached()), 'still following the new scan').toBe(true);
  expect(await app(page, (a) => a.log.some((e) => e.text.startsWith('Scan cancelled'))), 'no finished line').toBe(false);
  await expect(page.locator('.livepill')).toBeVisible();
  expect(rec.sse).toEqual([fx.SCAN_ID, NEW_ID]);
  expect(errors, 'page errors').toEqual([]);
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

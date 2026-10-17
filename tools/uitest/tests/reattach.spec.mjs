// F9a "Never lose netdiag": a reloaded page attaches to the scan that is
// watching (silently), Restart watching asks first, a stale tab cannot stop
// the watching, the heartbeat notices when netdiag stops or restarts, and
// only one tab beeps.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage } from '../support/mock.mjs';
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
  await page.goto('/');
  await page.getByRole('button', { name: 'Scan', exact: true }).click();
  await expect(page.locator('.livepill')).toContainText('Live');
  await expect(page.locator('#alerts div.banner:not(.more)')).toContainText('Device offline');
  await expect.poll(() => beeps(page), { message: 'the live notice beeps before the reload' }).toBeGreaterThan(0);

  // The server is watching; a reload replays its state.
  rec.running = true;
  rec.frames = fx.replayFrames();
  await page.reload();

  // 254 rows can take a while on a busy machine.
  await expect(page.locator('.livepill')).toContainText('Live', { timeout: 15_000 });
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
  await page.goto('/');
  await expect(page.getByRole('button', { name: 'Scan', exact: true })).toBeVisible();
  expect(rec.sse).toEqual([]);

  // Another tab (or a second launch) started watching after this page loaded.
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
  await expect(page.locator('.livepill')).toContainText('Live');
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
  await page.goto('/');
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
  await expect(page.locator('.livepill')).toContainText('Live');
  expect(posts(rec), 'Esc sends nothing').toHaveLength(0);

  await btn.click();
  await expect(dlg).toHaveJSProperty('open', true);
  await dlg.getByRole('button', { name: 'Restart', exact: true }).click();
  await expect(dlg).toHaveJSProperty('open', false);
  await expect.poll(() => posts(rec).length).toBe(1);
  expect(JSON.parse(posts(rec)[0].body)).toMatchObject({ cidr: fx.CIDR, live: true, replaceLive: true });
  await expect(page.locator('.livepill')).toContainText('Live');
  expect(await app(page, (a) => a.log.some((e) => e.text.startsWith('Watching restarted: ')))).toBe(true);

  expect(errors, 'page errors').toEqual([]);
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

for (const theme of THEMES) {
  test(`restart dialog and netdiag banners are accessible ${theme}`, async ({ page }, testInfo) => {
    await initStorage(page, { theme, live: true });
    const rec = await installMocks(page, { scenario: 'live', running: true, frames: fx.replayFrames() });
    await page.goto('/');
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
  await page.goto('/');
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
  await page.goto('/');
  await expect(page.locator('.livepill')).toContainText('Live');
  // The mock stream ends after the replay, like a backend that went away.
  await expect.poll(() => app(page, (a) => a.sseErr)).toBe(true);
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
  await page.goto('/');
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
  await page.goto('/');
  await expect.poll(() => rec.identityCalls).toBeGreaterThan(0);
  await page.getByRole('button', { name: 'Quit', exact: true }).click();
  await expect(page.getByText('netdiag has stopped. You can close this tab.')).toBeVisible();
  rec.identity = 'down';
  await app(page, (a) => a.hbTick());
  await app(page, (a) => a.hbTick());
  await expect(page.locator('#backend-stopped')).toBeHidden();
});

test('only the most recently focused tab beeps', async ({ page }) => {
  const other = await page.context().newPage();
  for (const p of [page, other]) {
    await spyBeeps(p);
    await initStorage(p, { theme: 'dark', live: false });
  }
  const recA = await installMocks(page, { scenario: 'scan', settings: soundOn() });
  const recB = await installMocks(other, { scenario: 'scan', settings: soundOn() });
  await page.goto('/');
  await expect.poll(() => recA.identityCalls).toBeGreaterThan(0);
  await other.goto('/');
  await expect.poll(() => recB.identityCalls).toBeGreaterThan(0);
  await expect.poll(() => app(page, (a) => a.settings !== null)).toBe(true);
  await expect.poll(() => app(other, (a) => a.settings !== null)).toBe(true);

  const notify = (p, id) => app(p, (a, n) => a.onNotice({ id: n, severity: 'warn', title: 'Toner low', message: 'test' }, 'tabs'), id);

  // The tab opened last plays the sound; the other only shows the banner.
  await notify(page, 'n-1');
  await notify(other, 'n-1');
  await expect(page.locator('#alerts div.banner:not(.more)')).toHaveCount(1);
  await expect(other.locator('#alerts div.banner:not(.more)')).toHaveCount(1);
  expect(await beeps(page), 'first tab').toBe(0);
  expect(await beeps(other), 'last opened tab').toBeGreaterThan(0);

  // Focusing the first tab makes it the one that beeps.
  await page.evaluate(() => window.dispatchEvent(new Event('focus')));
  await expect.poll(() => app(other, (a) => a.isBeeper())).toBe(false);
  const before = await beeps(other);
  await notify(page, 'n-2');
  await notify(other, 'n-2');
  expect(await beeps(page), 'focused tab').toBeGreaterThan(0);
  expect(await beeps(other), 'other tab').toBe(before);

  // When the beeping tab closes, the one left beeps again.
  await page.close({ runBeforeUnload: true });
  await expect.poll(() => app(other, (a) => a.isBeeper())).toBe(true);
  await other.close();
  expect(recA.unmatched.concat(recB.unmatched)).toEqual([]);
});

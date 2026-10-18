// Session token (v0.6 4.1): netdiag opens the page at /#s=<token>; the page
// keeps the token, takes it out of the address bar and sends it with every
// action and the settings read. A page whose token is out of date (netdiag
// was started again) keeps showing the live view, but its buttons answer
// 401 and a banner says to double-click netdiag, which opens a fresh page.
// Such a page cannot read the settings either, so it stays silent until a
// fresh page lets it read them.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME, SESSION } from '../support/mock.mjs';
import { scanToFinish } from '../support/steps.mjs';
import * as fx from '../fixtures/index.mjs';
import { runAxe } from '../support/axe.mjs';
import { THEMES } from '../support/config.mjs';

const BANNER = 'Buttons on this page are turned off for safety. Double-click netdiag to open a fresh page.';

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

// The page's Alpine component, for checks the DOM does not show.
const app = (page, fn, arg) => page.evaluate(([src, a]) => {
  const el = document.querySelector('.shell');
  const data = el && el._x_dataStack ? el._x_dataStack[0] : null;
  if (!data) return null; // the page is still loading
  return new Function('app', 'arg', 'return (' + src + ')(app, arg)')(data, a);
}, [fn.toString(), arg]);

// The person's settings: alerts muted, another SNMP community.
function mutedSettings() {
  const s = fx.settings();
  s.alerts.sound = false;
  s.community = 'site-c0mm';
  return s;
}

test('the token leaves the address bar and rides on every action', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.goto(HOME);
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('netdiag');
  await expect.poll(() => new URL(page.url()).hash).toBe('');
  expect(new URL(page.url()).pathname).toBe('/');
  expect(await page.evaluate(() => localStorage.getItem('netdiag.session'))).toBe(SESSION);

  await scanToFinish(page);
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  await page.locator('#settings-dialog').getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.locator('#settings-dialog')).toHaveJSProperty('open', false);
  await page.locator('#tab-history').click();
  page.once('dialog', (d) => d.accept());
  await page.locator('#history table tbody tr').first().getByRole('button', { name: /Delete/ }).click();
  await expect.poll(() => rec.requests.some((r) => r.method === 'DELETE')).toBe(true);
  await page.getByRole('button', { name: 'Quit', exact: true }).click();
  await expect(page.getByText('netdiag has stopped. You can close this tab.')).toBeVisible();

  const kinds = new Set(rec.requests.map((r) => r.method + ' ' + r.path.replace(/\/api\/(scans|history)\/[^/]+/, '/api/$1/{id}')));
  for (const k of ['POST /api/scans', 'PUT /api/settings', 'DELETE /api/history/{id}', 'POST /api/quit']) {
    expect(kinds, k).toContain(k);
  }
  expect(rec.requests.filter((r) => r.session !== SESSION), 'actions without the token').toEqual([]);
  expect(rec.unauthorized).toEqual([]);
  expect(rec.unmatched).toEqual([]);
  await expect(page.locator('#session-off')).toBeHidden();
});

test('the ?s= fallback works the same way', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.goto('/?s=' + SESSION);
  await expect.poll(() => new URL(page.url()).search).toBe('');
  await scanToFinish(page);
  expect(rec.requests.find((r) => r.path === '/api/scans').session).toBe(SESSION);
  expect(rec.unauthorized).toEqual([]);
});

test('a page with an old token shows the buttons-off banner and keeps the view; a fresh page turns buttons back on', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  // netdiag was started again: it expects a new token; this page has the old one.
  const rec = await installMocks(page, { scenario: 'scan', settings: mutedSettings(), session: 'new-token', stale: true });
  await page.goto(HOME);
  const banner = page.locator('#session-off');
  // The settings read already needs the token.
  await expect(banner).toBeVisible();
  await expect(banner).toHaveText(BANNER);
  await expect(banner).toHaveAttribute('role', 'status');
  expect(rec.unauthorized).toContain('GET /api/settings (X-Netdiag-Session: wrong)');
  // Read-only views keep working.
  await expect(page.getByLabel('Target (CIDR or range)')).toHaveValue(fx.CIDR);

  // The buttons look off (aria-disabled), as the banner says; a click still
  // reaches netdiag, which refuses it, and the page shows no second,
  // differently worded error: the banner already says what to do.
  const scan = page.getByRole('button', { name: 'Scan', exact: true });
  await expect(scan).toHaveAttribute('aria-disabled', 'true');
  await scan.click({ force: true });
  await expect.poll(() => rec.unauthorized.some((u) => u.startsWith('POST /api/scans'))).toBe(true);
  await expect(banner).toBeVisible();
  await expect(page.locator('main > .alert.error')).toBeHidden();
  // Quit cannot stop netdiag from this page, and the page does not claim it did.
  await page.getByRole('button', { name: 'Quit', exact: true }).click({ force: true });
  await expect.poll(() => rec.unauthorized.some((u) => u.startsWith('POST /api/quit'))).toBe(true);
  await expect(page.getByText('netdiag has stopped. You can close this tab.')).toBeHidden();
  await expect(banner).toBeVisible();

  // A second launch opens a fresh page with the current token; this tab
  // picks it up from storage and its buttons work again. It also reads the
  // settings it could not read before: Settings and Mute work, and a scan
  // uses the person's SNMP community.
  const fresh = await page.context().newPage();
  const recFresh = await installMocks(fresh, { scenario: 'scan', settings: mutedSettings(), session: 'new-token' });
  await fresh.goto('/#s=new-token');
  await expect(fresh.getByRole('heading', { level: 1 })).toHaveText('netdiag');
  await expect(fresh.locator('#session-off')).toBeHidden();
  expect(recFresh.unauthorized).toEqual([]);
  await expect(banner).toBeHidden();
  await expect.poll(() => app(page, (a) => a.settings && a.settings.community)).toBe('site-c0mm');
  expect(await app(page, (a) => [a.settingsLocked, a.community])).toEqual([false, 'site-c0mm']);
  await expect(page.getByRole('button', { name: 'Mute alerts' })).toBeEnabled();
  await scanToFinish(page);
  const last = rec.requests.filter((r) => r.path === '/api/scans').at(-1);
  expect(last.session).toBe('new-token');
  expect(JSON.parse(last.body).community).toBe('site-c0mm');
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  const dlg = page.locator('#settings-dialog');
  await expect(dlg.getByRole('alert')).toBeHidden();
  await expect(dlg.getByRole('button', { name: 'Save', exact: true })).toBeEnabled();
  await page.keyboard.press('Escape');
  await expect(dlg).toHaveJSProperty('open', false);
  expect(rec.unmatched).toEqual([]);
});

test('a page with an old token makes no sound for a live notice and leaves the beeping to others', async ({ page }) => {
  await spyBeeps(page);
  await initStorage(page, { theme: 'dark', live: true });
  // The person muted alerts. This page reattaches to the live scan, but may
  // not read the settings, so it cannot know that.
  const rec = await installMocks(page, { scenario: 'live', settings: mutedSettings(), session: 'new-token', stale: true,
    running: true, frames: fx.replayFrames() });
  await page.goto(HOME);
  await expect(page.locator('.livepill')).toBeVisible({ timeout: 15_000 });
  await expect(page.locator('#session-off')).toBeVisible();
  await expect.poll(() => app(page, (a) => a.settingsLocked)).toBe(true);
  expect(await app(page, (a) => [a.attached(), a.isBeeper(), a.soundOn()])).toEqual([true, false, false]);

  const fault = { id: 'n-70', trigger: 'printerOffline', severity: 'warn', title: 'Printer offline', message: 'test',
    key: 'printerOffline|00:1B:A9:12:34:56|', at: new Date().toISOString() };
  await app(page, (a, n) => a.onNotice(n, a.scanId), fault);
  await expect(page.locator('#alerts div.banner:not(.more)').filter({ hasText: 'Printer offline' })).toHaveCount(1);
  await page.waitForTimeout(300);
  expect(await beeps(page), 'beeps on a page that may not read the settings').toBe(0);
  // Mute shows the page is silent; it cannot be pressed here.
  const mute = page.getByRole('button', { name: 'Mute alerts' });
  await expect(mute).toHaveAttribute('aria-pressed', 'true');
  await expect(mute).toBeDisabled();
  // Settings say why they cannot be changed, and Esc gives focus back.
  const open = page.getByRole('button', { name: 'Settings', exact: true });
  await open.click();
  const dlg = page.locator('#settings-dialog');
  await expect(dlg.getByRole('alert')).toHaveText('Settings are turned off on this page for safety. Double-click netdiag to open a fresh page.');
  // Only the explanation and Close: no blank form to type into that could
  // not be saved.
  await expect(dlg.locator('input, select')).toHaveCount(0);
  await expect(dlg.getByRole('button', { name: 'Save', exact: true })).toBeHidden();
  await expect(dlg.getByRole('button', { name: 'Close', exact: true })).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(dlg).toHaveJSProperty('open', false);
  await expect(open).toBeFocused();
  await open.click();
  await dlg.getByRole('button', { name: 'Close', exact: true }).click();
  await expect(dlg).toHaveJSProperty('open', false);
  await expect(open).toBeFocused();
  // The Problems right now card needs no token: netdiag applies the
  // person's alert types itself (GET /api/problems), so this page shows it.
  await expect.poll(() => app(page, (a) => !!a.problems)).toBe(true);
  expect(rec.unauthorized.filter((u) => u.includes('/api/problems'))).toEqual([]);

  // A fresh page brings the token: this page reads the settings, still
  // makes no sound (the person muted alerts), and may take the beeping.
  const fresh = await page.context().newPage();
  await installMocks(fresh, { scenario: 'live', settings: mutedSettings(), session: 'new-token', running: true, frames: fx.replayFrames() });
  await fresh.goto('/#s=new-token');
  await expect(page.locator('#session-off')).toBeHidden();
  await expect.poll(() => app(page, (a) => a.settings !== null && !a.settingsLocked)).toBe(true);
  await expect(mute).toBeEnabled();
  await expect(mute).toHaveAttribute('aria-pressed', 'true');
  await app(page, (a, n) => a.onNotice(n, a.scanId), { ...fault, id: 'n-71' });
  await page.waitForTimeout(300);
  expect(await beeps(page), 'beeps with alerts muted').toBe(0);
  await page.evaluate(() => window.dispatchEvent(new Event('focus')));
  await expect.poll(() => app(page, (a) => a.isBeeper())).toBe(true);
  await fresh.close();
  expect(rec.unmatched).toEqual([]);
});

// Settings opened on a stale page shows only why. When a fresh page brings
// the token while it is open, the form comes from the real settings, never
// from the blank stand-in, which Save would write over every setting.
test('Settings opened while locked shows the real settings once a fresh page brings the token', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan', settings: mutedSettings(), session: 'new-token', stale: true });
  await page.goto(HOME);
  await expect(page.locator('#session-off')).toBeVisible();
  await expect.poll(() => app(page, (a) => a.settingsLocked)).toBe(true);
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  const dlg = page.locator('#settings-dialog');
  await expect(dlg.getByRole('alert')).toHaveText('Settings are turned off on this page for safety. Double-click netdiag to open a fresh page.');
  await expect(dlg.locator('input, select')).toHaveCount(0);
  await expect(dlg.getByRole('button', { name: 'Save', exact: true })).toBeHidden();

  // A second launch opened a fresh page: its token reaches this tab
  // through storage while Settings is still open.
  await page.evaluate(() => {
    localStorage.setItem('netdiag.session', 'new-token');
    window.dispatchEvent(new StorageEvent('storage', { key: 'netdiag.session', newValue: 'new-token' }));
  });
  await expect(dlg.getByLabel('SNMP community')).toHaveValue('site-c0mm');
  await expect(dlg.getByRole('alert')).toBeHidden();
  await expect(dlg.getByLabel('Play a sound')).not.toBeChecked();
  const save = dlg.getByRole('button', { name: 'Save', exact: true });
  await expect(save).toBeEnabled();
  await expect(dlg.getByRole('button', { name: 'Cancel', exact: true })).toBeVisible();

  // Save sends the person's settings back, not a blank form.
  await save.click();
  await expect(dlg).toHaveJSProperty('open', false);
  const put = rec.requests.filter((r) => r.method === 'PUT' && r.path === '/api/settings').pop();
  expect(put.session).toBe('new-token');
  const doc = JSON.parse(put.body);
  const want = mutedSettings();
  expect(doc.community).toBe(want.community);
  expect(doc.alerts).toEqual(want.alerts);
  expect(doc.mcp).toEqual(want.mcp);
  expect(rec.unmatched).toEqual([]);
});

// The blank stand-in is never saved, even if something submits the form.
test('a locked Settings never saves', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan', session: 'new-token', stale: true });
  await page.goto(HOME);
  await expect.poll(() => app(page, (a) => a.settingsLocked)).toBe(true);
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  await expect(page.locator('#settings-dialog')).toHaveJSProperty('open', true);
  await app(page, (a) => a.saveSettings());
  expect(rec.requests.filter((r) => r.method === 'PUT'), 'PUT /api/settings').toEqual([]);
});

for (const theme of THEMES) {
  test(`the buttons-off banner is accessible ${theme}`, async ({ page }, testInfo) => {
    await initStorage(page, { theme, live: false });
    await installMocks(page, { scenario: 'scan', session: 'new-token', stale: true });
    await page.goto(HOME);
    await expect(page.locator('#session-off')).toBeVisible();
    await runAxe(page, `session-off-${theme}`, testInfo);
  });
}

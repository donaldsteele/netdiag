// Session token (v0.6 4.1): netdiag opens the page at /#s=<token>; the page
// keeps the token, takes it out of the address bar and sends it with every
// action and the settings read. A page whose token is out of date (netdiag
// was started again) keeps showing the live view, but its buttons answer
// 401 and a banner says to double-click netdiag, which opens a fresh page.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME, SESSION } from '../support/mock.mjs';
import { scanToFinish } from '../support/steps.mjs';
import * as fx from '../fixtures/index.mjs';
import { runAxe } from '../support/axe.mjs';
import { THEMES } from '../support/config.mjs';

const BANNER = 'Buttons on this page are turned off for safety. Double-click netdiag to open a fresh page.';

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
  const rec = await installMocks(page, { scenario: 'scan', session: 'new-token', stale: true });
  await page.goto(HOME);
  const banner = page.locator('#session-off');
  // The settings read already needs the token.
  await expect(banner).toBeVisible();
  await expect(banner).toHaveText(BANNER);
  await expect(banner).toHaveAttribute('role', 'status');
  expect(rec.unauthorized).toContain('GET /api/settings (X-Netdiag-Session: wrong)');
  // Read-only views keep working.
  await expect(page.getByLabel('Target (CIDR or range)')).toHaveValue(fx.CIDR);

  await page.getByRole('button', { name: 'Scan', exact: true }).click();
  await expect.poll(() => rec.unauthorized.some((u) => u.startsWith('POST /api/scans'))).toBe(true);
  await expect(banner).toBeVisible();
  // Quit cannot stop netdiag from this page, and the page does not claim it did.
  await page.getByRole('button', { name: 'Quit', exact: true }).click();
  await expect.poll(() => rec.unauthorized.some((u) => u.startsWith('POST /api/quit'))).toBe(true);
  await expect(page.getByText('netdiag has stopped. You can close this tab.')).toBeHidden();
  await expect(banner).toBeVisible();

  // A second launch opens a fresh page with the current token; this tab
  // picks it up from storage and its buttons work again.
  const fresh = await page.context().newPage();
  const recFresh = await installMocks(fresh, { scenario: 'scan', session: 'new-token' });
  await fresh.goto('/#s=new-token');
  await expect(fresh.getByRole('heading', { level: 1 })).toHaveText('netdiag');
  await expect(fresh.locator('#session-off')).toBeHidden();
  expect(recFresh.unauthorized).toEqual([]);
  await expect(banner).toBeHidden();
  await scanToFinish(page);
  expect(rec.requests.filter((r) => r.path === '/api/scans').at(-1).session).toBe('new-token');
  expect(rec.unmatched).toEqual([]);
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

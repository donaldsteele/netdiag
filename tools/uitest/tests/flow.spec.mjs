// Flow smoke tests against mocked /api/* and SSE. These must pass in every milestone.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME, SESSION } from '../support/mock.mjs';
import { scanToFinish } from '../support/steps.mjs';
import * as fx from '../fixtures/index.mjs';

const stats = fx.expectedStats();

function watchErrors(page) {
  const errors = [];
  page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`));
  page.on('console', (m) => {
    if (m.type() === 'error' && !/favicon/i.test(m.text())) errors.push(`console: ${m.text()}`);
  });
  return errors;
}

function assertClean(rec, errors) {
  expect(errors, 'page errors').toEqual([]);
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
  expect(rec.external, 'external requests').toEqual([]);
  // Every POST, PUT and DELETE carries this launch's session token (v0.6 4.1).
  expect(rec.requests.filter((r) => r.session !== SESSION), 'requests without the session token').toEqual([]);
  expect(rec.unauthorized, 'requests the session guard refused').toEqual([]);
}

test('scan flow: load, scan, hosts render, history, compare, settings', async ({ page }) => {
  const errors = watchErrors(page);
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.goto(HOME);

  await expect(page.getByRole('heading', { level: 1 })).toHaveText('netdiag');
  await expect(page.getByLabel('Target (CIDR or range)')).toHaveValue(fx.CIDR);

  await scanToFinish(page);
  await expect(page.locator("#tile-alive .tv")).toHaveText(String(stats.alive));
  await expect(page.locator("#tile-printers .tv")).toHaveText(String(stats.printers));
  await expect(page.locator("#tile-web .tv")).toHaveText(String(stats.web));

  // The scan POST must carry the JSON content type the server requires.
  const post = rec.requests.find((r) => r.method === 'POST' && r.path === '/api/scans');
  expect(post, 'POST /api/scans').toBeTruthy();
  expect(post.contentType).toBe('application/json');
  expect(JSON.parse(post.body)).toMatchObject({ cidr: fx.CIDR, live: false });

  // History tab lists every saved scan, including the unreadable one.
  await page.locator('#tab-history').click();
  const history = page.locator('#history');
  await expect(history).toBeVisible();
  await expect(history.locator('table tbody tr')).toHaveCount(fx.history().length);

  // Compare the default pair and check the diff sections.
  await history.getByRole('button', { name: 'Compare', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Added (1)' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Removed (1)' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Changed (2)' })).toBeVisible();

  // Settings dialog opens, Escape closes it, and Save sends a JSON PUT.
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  const dialog = page.locator('#settings-dialog');
  await expect(dialog).toHaveJSProperty('open', true);
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(dialog).toHaveJSProperty('open', false);
  const put = rec.requests.find((r) => r.method === 'PUT' && r.path === '/api/settings');
  expect(put, 'PUT /api/settings').toBeTruthy();
  expect(put.contentType).toBe('application/json');

  // Theme toggle flips the attribute and persists the choice.
  await page.locator('.themebtn').click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  expect(await page.evaluate(() => localStorage.getItem('netdiag.theme'))).toBe('light');

  assertClean(rec, errors);
});

test('live flow: new device, offline transition, conflict and notice', async ({ page }) => {
  const errors = watchErrors(page);
  await initStorage(page, { theme: 'dark', live: true });
  const rec = await installMocks(page, { scenario: 'live' });
  await page.goto(HOME);

  await page.getByRole('button', { name: 'Scan', exact: true }).click();
  await expect(page.locator('.livepill')).toContainText('Live');
  await expect(page.getByRole('button', { name: 'Stop live', exact: true })).toBeVisible();

  // 253 snapshot hosts plus the one discovered live.
  const rows = page.locator('#hosts tbody tr.host');
  await expect(rows).toHaveCount(fx.TOTAL_HOSTS);
  await expect(page.locator('#hosts tr.host.isnew')).toHaveCount(1);
  await expect(page.locator('#hosts tr.host').filter({ hasText: '192.168.2.254' }).locator('.badge.new')).toBeVisible();

  await expect(page.locator('#hosts tr.host').filter({ hasText: '192.168.2.200' }).locator('.badge.offline')).toBeVisible();
  await expect(page.locator('div.sr[role="status"]')).toHaveText('Device went offline: 192.168.2.200 VMware, Inc.');

  await expect(page.locator('#hosts tr.host').filter({ hasText: '192.168.2.30' }).locator('.badge.conflict')).toBeVisible();
  await expect(page.locator('#alerts div.banner:not(.more)')).toContainText('Device offline');

  // The live stream never sends done, so the scan stays in live state.
  await expect(page.locator('section.progress .stage')).toContainText('watching for new devices');
  assertClean(rec, errors);
});

test('app shell: keyboard sidebar nav, view persistence, responsive rail and hamburger', async ({ page }) => {
  const errors = watchErrors(page);
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(HOME);

  // Desktop: labelled sidebar, Devices is current by default.
  const nav = page.getByRole('navigation', { name: 'Main' });
  await expect(nav.locator('#tab-devices')).toHaveAttribute('aria-current', 'page');
  await expect(nav.locator('#tab-devices .lbl')).toBeVisible();
  expect((await page.locator('.sidebar').boundingBox()).width).toBeGreaterThan(200);

  // Keyboard: focus a nav item and activate with Enter / Space.
  await nav.locator('#tab-health').focus();
  await page.keyboard.press('Enter');
  await expect(page.locator('#health')).toBeVisible();
  await expect(page.locator('#hosts')).toBeHidden();
  await expect(nav.locator('#tab-health')).toHaveAttribute('aria-current', 'page');
  await nav.locator('#tab-history').focus();
  await page.keyboard.press('Space');
  await expect(page.locator('#history')).toBeVisible();
  await nav.locator('#tab-activity').focus();
  await page.keyboard.press('Enter');
  await expect(page.locator('#activity')).toBeVisible();

  // Last view is remembered across reloads.
  await page.reload();
  await expect(page.locator('#activity')).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem('netdiag.view'))).toBe('activity');
  await nav.locator('#tab-devices').focus();
  await page.keyboard.press('Enter');
  await expect(page.locator('#hosts')).toBeVisible();

  // Settings entry opens the dialog; Escape closes it.
  await nav.locator('#nav-settings').click();
  await expect(page.locator('#settings-dialog')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.locator('#settings-dialog')).toBeHidden();

  // Export menu opens and holds both formats.
  await page.locator('details.menu summary').click();
  await expect(page.getByRole('button', { name: 'Export JSON' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Export CSV' })).toBeVisible();

  // Rail (<1100px): 56px wide, labels visually hidden but names kept.
  await page.setViewportSize({ width: 900, height: 800 });
  expect((await page.locator('.sidebar').boundingBox()).width).toBeLessThan(80);
  await expect(page.getByRole('button', { name: 'Health', exact: true }).first()).toBeVisible();
  await expect(nav.locator('#tab-health')).toHaveAttribute('title', 'Health');
  await expect(page.locator('.menubtn')).toBeHidden();

  // Hamburger (<760px): sidebar collapsed until Menu is pressed.
  await page.setViewportSize({ width: 390, height: 844 });
  const menu = page.locator('.menubtn');
  await expect(menu).toBeVisible();
  await expect(menu).toHaveAttribute('aria-expanded', 'false');
  await expect(page.locator('.sidebar')).toBeHidden();
  await menu.focus();
  await page.keyboard.press('Enter');
  await expect(menu).toHaveAttribute('aria-expanded', 'true');
  await expect(page.locator('.sidebar')).toBeVisible();
  await nav.locator('#tab-history').focus();
  await page.keyboard.press('Enter');
  await expect(page.locator('#history')).toBeVisible();
  await expect(page.locator('.sidebar')).toBeHidden();
  await expect(menu).toHaveAttribute('aria-expanded', 'false');

  assertClean(rec, errors);
});

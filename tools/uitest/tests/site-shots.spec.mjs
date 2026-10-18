// Screenshots for the GitHub Pages site (docs/index.html). Captured at 2x for sharp
// display on high-density screens and written straight to docs/assets/shots/.
// v0.6: the Devices view shows the Problems right now card, the health
// cards say what a result means, the Activity log has a Fixed line, and
// chat-connect.png is the team chat wizard's fingerprint step. The real
// Zebra shots (real-zebra-*.png) and bubbles-resolved.png come from real
// hardware and a bubbles server (docs/field-checklist.md 6.1), not from here.
import path from 'node:path';
import fs from 'node:fs/promises';
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME } from '../support/mock.mjs';
import { scanToFinish } from '../support/steps.mjs';
import { UITEST_DIR } from '../support/config.mjs';
import * as fx from '../fixtures/index.mjs';

const SHOTS = path.resolve(UITEST_DIR, '..', '..', 'docs', 'assets', 'shots');
const SIZE = { width: 1280, height: 800 };

test.use({ deviceScaleFactor: 2 });

async function prepare(page, theme, opts = {}) {
  await initStorage(page, { theme, live: false });
  await installMocks(page, { scenario: 'scan', ...opts });
  await page.setViewportSize(SIZE);
  await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: theme });
  await page.goto(HOME);
  await scanToFinish(page);
  await fs.mkdir(SHOTS, { recursive: true });
}

const shot = (page, name) => page.screenshot({ path: path.join(SHOTS, name + '.png') });

for (const theme of ['dark', 'light']) {
  test(`site: devices ${theme}`, async ({ page }) => {
    await prepare(page, theme);
    await expect(page.locator('#problems-now .pitem').first()).toBeVisible();
    await shot(page, 'devices-' + theme);
  });
}

test('site: printer drawer', async ({ page }) => {
  await prepare(page, 'dark');
  await page.locator('#tile-printers').click();
  await page.locator('#hosts tbody tr.host').first().locator('button.rowbtn').click();
  await expect(page.locator('#drawer .supplies li').first()).toBeVisible();
  await shot(page, 'drawer');
});

test('site: health', async ({ page }) => {
  await prepare(page, 'dark');
  await page.locator('#tab-health').click();
  await expect(page.locator('.hcard').first()).toBeVisible();
  // The weak Wi-Fi card says what it means and what to try.
  await expect(page.locator('.hcard .advice').first()).toBeVisible();
  await shot(page, 'health');
});

test('site: activity', async ({ page }) => {
  // The scan's toner-low notice, then the OK notice that closes it, now
  // (so the Fixed time matches the log's), 12 minutes after it opened.
  const now = Date.now();
  const ok = { ...fx.fixedNotice(1), at: new Date(now).toISOString(), openedAt: new Date(now - 12 * 60000).toISOString() };
  const frames = fx.scanFrames();
  await prepare(page, 'dark', { frames: [...frames.slice(0, -1), { type: 'notice', data: ok }, frames.at(-1)] });
  await page.locator('#tab-activity').click();
  await expect(page.locator('#log .logline').first()).toBeVisible();
  await expect(page.locator('#log')).toContainText('Fixed: Supply OK again');
  await page.evaluate(() => scrollTo(0, 0));
  await shot(page, 'activity');
});

test('site: history compare', async ({ page }) => {
  await prepare(page, 'dark');
  await page.locator('#tab-history').click();
  await page.getByRole('button', { name: 'Compare', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Added (1)' })).toBeVisible();
  await shot(page, 'history');
});

// The page reads the settings as it loads; Settings opened before that
// starts from the defaults.
async function openSettings(page) {
  await expect.poll(() => page.evaluate(() => !!window.Alpine.$data(document.querySelector('.shell')).settings)).toBe(true);
  await page.locator('#nav-settings').click();
  const dialog = page.locator('#settings-dialog');
  await expect(dialog).toBeVisible();
  return dialog;
}

test('site: settings MCP', async ({ page }) => {
  await prepare(page, 'dark');
  let dialog = await openSettings(page);
  await dialog.locator('#mcp-enabled').check();
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(dialog).toBeHidden();
  dialog = await openSettings(page);
  await expect(dialog.locator('#mcp-endpoint')).toBeVisible();
  const panel = dialog.locator('fieldset', { has: page.locator('#mcp-enabled') });
  await panel.scrollIntoViewIfNeeded();
  await panel.screenshot({ path: path.join(SHOTS, 'settings-mcp.png') });
});

test('site: connect to team chat', async ({ page }) => {
  await prepare(page, 'dark');
  const dialog = await openSettings(page);
  await dialog.locator('#chat-connect').click();
  const wizard = page.locator('#chat-dialog');
  await wizard.getByRole('button', { name: 'Use this server: Office' }).click();
  await expect(wizard.locator('#wiz-fp-start')).toHaveText(fx.CHAT_GROUPS.slice(0, 4).join(' '));
  await wizard.screenshot({ path: path.join(SHOTS, 'chat-connect.png') });
});

test('site: team chat connected', async ({ page }) => {
  await prepare(page, 'dark', { chat: 'connected', settings: { ...fx.settings(), chat: { enabled: true, minSeverity: 'warning' } } });
  const dialog = await openSettings(page);
  const section = dialog.locator('#set-chat');
  await expect(section.locator('#chat-status')).toHaveText('Status: Connected');
  await section.scrollIntoViewIfNeeded();
  await section.screenshot({ path: path.join(SHOTS, 'settings-chat.png') });
});

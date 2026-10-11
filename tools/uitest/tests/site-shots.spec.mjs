// Screenshots for the GitHub Pages site (docs/index.html). Captured at 2x for sharp
// display on high-density screens and written straight to docs/assets/shots/.
import path from 'node:path';
import fs from 'node:fs/promises';
import { test, expect } from '@playwright/test';
import { installMocks, initStorage } from '../support/mock.mjs';
import { scanToFinish } from '../support/steps.mjs';
import { UITEST_DIR } from '../support/config.mjs';

const SHOTS = path.resolve(UITEST_DIR, '..', '..', 'docs', 'assets', 'shots');
const SIZE = { width: 1280, height: 800 };

test.use({ deviceScaleFactor: 2 });

async function prepare(page, theme) {
  await initStorage(page, { theme, live: false });
  await installMocks(page, { scenario: 'scan' });
  await page.setViewportSize(SIZE);
  await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: theme });
  await page.goto('/');
  await scanToFinish(page);
  await fs.mkdir(SHOTS, { recursive: true });
}

const shot = (page, name) => page.screenshot({ path: path.join(SHOTS, name + '.png') });

for (const theme of ['dark', 'light']) {
  test(`site: devices ${theme}`, async ({ page }) => {
    await prepare(page, theme);
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
  await shot(page, 'health');
});

test('site: activity', async ({ page }) => {
  await prepare(page, 'dark');
  await page.locator('#tab-activity').click();
  await expect(page.locator('#log .logline').first()).toBeVisible();
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

test('site: settings MCP', async ({ page }) => {
  await prepare(page, 'dark');
  await page.locator('#nav-settings').click();
  const dialog = page.locator('#settings-dialog');
  await dialog.locator('#mcp-enabled').check();
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(dialog).toBeHidden();
  await page.locator('#nav-settings').click();
  await expect(dialog.locator('#mcp-endpoint')).toBeVisible();
  const panel = dialog.locator('fieldset', { has: page.locator('#mcp-enabled') });
  await panel.scrollIntoViewIfNeeded();
  await panel.screenshot({ path: path.join(SHOTS, 'settings-mcp.png') });
});

// T9: README screenshots. Output (git-ignored): out/docs/devices-dark.png and
// out/docs/drawer-light.png. Copy them to docs/img/ by hand after a run (see README).
// Viewport shots (not full page) at deviceScaleFactor 1 keep each PNG well under 250 KB.
import path from 'node:path';
import fs from 'node:fs/promises';
import { test, expect } from '@playwright/test';
import { installMocks, initStorage } from '../support/mock.mjs';
import { scanToFinish } from '../support/steps.mjs';
import { OUT_DIR } from '../support/config.mjs';

const DOCS_OUT = path.join(OUT_DIR, 'docs');
const SIZE = { width: 1280, height: 800 };

async function prepare(page, theme) {
  await initStorage(page, { theme, live: false });
  await installMocks(page, { scenario: 'scan' });
  await page.setViewportSize(SIZE);
  await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: theme });
  await page.goto('/');
  await scanToFinish(page);
}

test('docs screenshot: devices view, dark theme', async ({ page }) => {
  await prepare(page, 'dark');
  await fs.mkdir(DOCS_OUT, { recursive: true });
  await page.screenshot({ path: path.join(DOCS_OUT, 'devices-dark.png'), scale: 'css' });
});

test('docs screenshot: device drawer, light theme', async ({ page }) => {
  await prepare(page, 'light');
  await page.locator('#tile-printers').click();
  await page.locator('#hosts tbody tr.host').first().locator('button.rowbtn').click();
  const drawer = page.locator('#drawer');
  await expect(drawer).toBeVisible();
  await expect(drawer.locator('.supplies li').first()).toBeVisible();
  await fs.mkdir(DOCS_OUT, { recursive: true });
  await page.screenshot({ path: path.join(DOCS_OUT, 'drawer-light.png'), scale: 'css' });
});

test('docs screenshot: settings, MCP agent access, dark theme', async ({ page }) => {
  await prepare(page, 'dark');
  await page.locator('#nav-settings').click();
  const dialog = page.locator('#settings-dialog');
  await expect(dialog).toBeVisible();
  await dialog.locator('#mcp-enabled').check();
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(dialog).toBeHidden();
  await page.locator('#nav-settings').click();
  await expect(dialog.locator('#mcp-endpoint')).toBeVisible();
  await dialog.locator('#mcp-enabled').scrollIntoViewIfNeeded();
  await fs.mkdir(DOCS_OUT, { recursive: true });
  const out = path.join(DOCS_OUT, 'settings-mcp.png');
  await dialog.screenshot({ path: out, scale: 'css' });
  await fs.mkdir(path.resolve('..', '..', 'docs', 'img'), { recursive: true });
  await fs.copyFile(out, path.resolve('..', '..', 'docs', 'img', 'settings-mcp.png'));
});

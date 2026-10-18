// T9: README screenshots, written to out/docs/ and copied into docs/img/.
// Viewport shots (not full page) at deviceScaleFactor 1 keep each PNG well under 250 KB.
// v0.6: the Devices view with the Problems right now card, the drawer's
// conflict view (the two devices side by side), Settings > Team chat
// (bubbles) connected, and the connect wizard's fingerprint step.
import path from 'node:path';
import fs from 'node:fs/promises';
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME } from '../support/mock.mjs';
import { scanToFinish } from '../support/steps.mjs';
import { OUT_DIR, UITEST_DIR } from '../support/config.mjs';
import * as fx from '../fixtures/index.mjs';

const DOCS_OUT = path.join(OUT_DIR, 'docs');
const DOCS_IMG = path.resolve(UITEST_DIR, '..', '..', 'docs', 'img');
const SIZE = { width: 1280, height: 800 };

async function prepare(page, theme, opts = {}) {
  await initStorage(page, { theme, live: false });
  const rec = await installMocks(page, { scenario: 'scan', ...opts });
  await page.setViewportSize(SIZE);
  await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: theme });
  await page.goto(HOME);
  await scanToFinish(page);
  return rec;
}

// save writes the shot to out/docs/<name>.png and copies it into docs/img/.
async function save(target, name) {
  await fs.mkdir(DOCS_OUT, { recursive: true });
  const out = path.join(DOCS_OUT, name + '.png');
  await target.screenshot({ path: out, scale: 'css' });
  await fs.mkdir(DOCS_IMG, { recursive: true });
  await fs.copyFile(out, path.join(DOCS_IMG, name + '.png'));
}

// The page reads the settings as it loads; Settings opened before that
// starts from the defaults.
async function openSettings(page) {
  await expect.poll(() => page.evaluate(() => !!window.Alpine.$data(document.querySelector('.shell')).settings)).toBe(true);
  await page.locator('#nav-settings').click();
  const dialog = page.locator('#settings-dialog');
  await expect(dialog).toBeVisible();
  return dialog;
}

test('docs screenshot: devices view, dark theme', async ({ page }) => {
  await prepare(page, 'dark');
  await expect(page.locator('#problems-now .pitem').first()).toBeVisible();
  await save(page, 'devices-dark');
});

test('docs screenshot: device drawer with a conflict, light theme', async ({ page }) => {
  await prepare(page, 'light', { frames: fx.framesWith(fx.flapHosts(2), [fx.flapConflict(2)]) });
  await page.locator(`#hosts button.rowbtn[data-ip="${fx.FLAP_IP}"]`).click();
  const drawer = page.locator('#drawer');
  await expect(drawer).toBeVisible();
  await expect(drawer.locator('.cbox .chead strong')).toHaveText('Conflict');
  await expect(drawer.locator('.cpair .ccard')).toHaveCount(2);
  await drawer.locator('.cbox').scrollIntoViewIfNeeded();
  await save(page, 'drawer-light');
});

test('docs screenshot: settings, MCP agent access, dark theme', async ({ page }) => {
  await prepare(page, 'dark');
  let dialog = await openSettings(page);
  await dialog.locator('#mcp-enabled').check();
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(dialog).toBeHidden();
  dialog = await openSettings(page);
  await expect(dialog.locator('#mcp-endpoint')).toBeVisible();
  await dialog.locator('#mcp-enabled').scrollIntoViewIfNeeded();
  await save(dialog, 'settings-mcp');
});

test('docs screenshot: settings, team chat connected, dark theme', async ({ page }) => {
  await prepare(page, 'dark', { chat: 'connected', settings: { ...fx.settings(), chat: { enabled: true, minSeverity: 'warning' } } });
  const dialog = await openSettings(page);
  const section = dialog.locator('#set-chat');
  await expect(section.locator('#chat-status')).toHaveText('Status: Connected');
  await expect(section.locator('#chat-where')).toContainText('#' + fx.CHAT_CHANNEL.name);
  await expect(section.locator('#chat-enabled')).toBeChecked();
  await section.scrollIntoViewIfNeeded();
  await save(section, 'settings-chat');
});

test('docs screenshot: connect to team chat, the fingerprint step, dark theme', async ({ page }) => {
  await prepare(page, 'dark');
  const dialog = await openSettings(page);
  await dialog.locator('#chat-connect').click();
  const wizard = page.locator('#chat-dialog');
  await wizard.getByRole('button', { name: 'Use this server: Office' }).click();
  await expect(wizard.locator('#wiz-fp-start')).toHaveText(fx.CHAT_GROUPS.slice(0, 4).join(' '));
  await save(wizard, 'chat-wizard');
});

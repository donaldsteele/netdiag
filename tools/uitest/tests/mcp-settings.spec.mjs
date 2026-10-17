// T7b: Agent access (MCP) section of the Settings dialog.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage } from '../support/mock.mjs';
import * as fx from '../fixtures/index.mjs';
import { runAxe } from '../support/axe.mjs';
import { THEMES } from '../support/config.mjs';

// Team chat settings that are not the defaults, so a save that rebuilt them
// from defaults would show.
const CHAT = { enabled: true, minSeverity: 'critical' };

for (const theme of THEMES) {
  test(`mcp settings ${theme}`, async ({ page }, testInfo) => {
    await initStorage(page, { theme, live: false });
    const rec = await installMocks(page, { scenario: 'scan', settings: { ...fx.settings(), chat: { ...CHAT } } });
    await page.goto('/');
    const dialog = page.locator('#settings-dialog');
    const sw = dialog.locator('#mcp-enabled');

    await page.locator('#nav-settings').click();
    await expect(dialog).toBeVisible();
    await expect(sw).not.toBeChecked();
    await expect(dialog.locator('#mcp-status')).toContainText('Disabled');
    await expect(dialog.locator('#mcp-endpoint')).toBeHidden();

    await sw.focus();
    await page.keyboard.press('Space');
    await expect(sw).toBeChecked();
    await expect(dialog.locator('#mcp-pending')).toBeVisible();
    await dialog.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(dialog).toBeHidden();
    const put = rec.requests.filter((r) => r.method === 'PUT' && r.path === '/api/settings').pop();
    expect(JSON.parse(put.body).mcp.enabled).toBe(true);

    await page.locator('#nav-settings').click();
    await expect(dialog.locator('#mcp-status')).toContainText('Enabled');
    await expect(dialog.locator('#mcp-endpoint')).toContainText('http://127.0.0.1:8787/mcp');
    await expect(dialog.locator('#mcp-pending')).toBeHidden();
    await dialog.getByRole('button', { name: 'Copy MCP endpoint' }).click();
    await expect(page.getByText('Copied MCP endpoint http://127.0.0.1:8787/mcp')).toBeAttached();
    await runAxe(page, `settings-mcp-${theme}`, testInfo);

    // Muting alerts afterwards must not disable MCP.
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toBeHidden();
    await page.getByRole('button', { name: 'Mute alerts' }).click();
    await expect.poll(() => rec.requests.filter((r) => r.method === 'PUT').length).toBeGreaterThan(1);
    const last = rec.requests.filter((r) => r.method === 'PUT').pop();
    expect(JSON.parse(last.body).mcp.enabled).toBe(true);
    // Nor reset the team chat settings.
    expect(JSON.parse(last.body).chat).toEqual(CHAT);
    expect(JSON.parse(put.body).chat).toEqual(CHAT);
  });
}

async function openSettings(page) {
  const menu = page.locator('.menubtn');
  if (await menu.isVisible() && (await menu.getAttribute('aria-expanded')) !== 'true') await menu.click();
  await page.locator('#nav-settings').click();
}

test('mcp endpoint row does not overflow on narrow screens', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { scenario: 'scan' });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/');
  const dialog = page.locator('#settings-dialog');
  await openSettings(page);
  await dialog.locator('#mcp-enabled').check();
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await openSettings(page);
  await expect(dialog.locator('#mcp-endpoint')).toBeVisible();
  const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(over).toBeLessThanOrEqual(0);
  const d = await dialog.evaluate((el) => el.scrollWidth - el.clientWidth);
  expect(d).toBeLessThanOrEqual(0);
});

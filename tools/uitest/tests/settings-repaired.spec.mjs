// F7: settings that were not valid in the settings file are reset when
// netdiag reads it, and Settings says so once, naming the fields.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage } from '../support/mock.mjs';
import { runAxe } from '../support/axe.mjs';
import { THEMES } from '../support/config.mjs';

const NOTE = 'Some settings were not valid and were reset: Supply threshold, Team chat alert level. Check them below.';

for (const theme of THEMES) {
  test(`repaired settings notice ${theme}`, async ({ page }, testInfo) => {
    await initStorage(page, { theme, live: false });
    const rec = await installMocks(page, { scenario: 'scan', repaired: ['alerts.supplyThresholdPct', 'chat.minSeverity'] });
    await page.goto('/');
    const dialog = page.locator('#settings-dialog');
    const note = dialog.locator('#settings-repaired');
    const nav = page.locator('#nav-settings');

    await nav.click();
    await expect(dialog).toBeVisible();
    await expect(note).toBeVisible();
    await expect(note).toHaveText(NOTE);
    await expect(note).toHaveAttribute('role', 'status');
    // A shape plus a word: the warning mark comes before the text.
    expect(await note.evaluate((el) => getComputedStyle(el, '::before').content)).toContain('!');
    // The reset field shows its default.
    await expect(dialog.getByLabel('Supply threshold (%, 1-100)')).toHaveValue('15');
    await runAxe(page, `settings-repaired-${theme}`, testInfo);

    // Esc closes Settings and returns focus; the notice does not come back.
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
    await expect(nav).toBeFocused();
    await nav.click();
    await expect(dialog).toBeVisible();
    await expect(note).toBeHidden();

    // Saving sends every section back unchanged, without the repaired list.
    await dialog.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(dialog).toBeHidden();
    const put = rec.requests.filter((r) => r.method === 'PUT' && r.path === '/api/settings').pop();
    const doc = JSON.parse(put.body);
    expect(doc.repaired).toBeUndefined();
    expect(doc.alerts.supplyThresholdPct).toBe(15);
    expect(doc.chat).toEqual({ enabled: false, minSeverity: 'warning' });
    expect(doc.mcp.enabled).toBe(false);
    expect(rec.unmatched).toEqual([]);
    expect(rec.external).toEqual([]);
  });
}

test('no repaired settings, no notice', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { scenario: 'scan' });
  await page.goto('/');
  await page.locator('#nav-settings').click();
  const dialog = page.locator('#settings-dialog');
  await expect(dialog).toBeVisible();
  await expect(dialog.locator('#settings-repaired')).toBeHidden();
});

test('unknown repaired field is named by its path', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { scenario: 'scan', repaired: ['alerts.cooldownSec', 'someNewField'] });
  await page.setViewportSize({ width: 360, height: 800 });
  await page.goto('/');
  const menu = page.locator('.menubtn');
  if (await menu.isVisible()) await menu.click();
  await page.locator('#nav-settings').click();
  const note = page.locator('#settings-repaired');
  await expect(note).toHaveText('Some settings were not valid and were reset: Repeat-alert cooldown, someNewField. Check them below.');
  const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(over).toBeLessThanOrEqual(0);
  const d = await page.locator('#settings-dialog').evaluate((el) => el.scrollWidth - el.clientWidth);
  expect(d).toBeLessThanOrEqual(0);
});

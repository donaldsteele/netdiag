// T7: screenshots + axe for empty state, health, history+diff, settings dialog in both themes.
import path from 'node:path';
import { test, expect } from '@playwright/test';
import { installMocks, initStorage } from '../support/mock.mjs';
import { runAxe } from '../support/axe.mjs';
import { scanToFinish } from '../support/steps.mjs';
import { OUT_DIR, THEMES } from '../support/config.mjs';

const shot = (page, name) => page.screenshot({ path: path.join(OUT_DIR, 'shots', `${name}.png`), fullPage: true });

for (const theme of THEMES) {
  test(`empty state ${theme}`, async ({ page }, testInfo) => {
    await initStorage(page, { theme, live: false });
    await installMocks(page, { scenario: 'scan' });
    await page.goto('/');
    await expect(page.getByText('> no scan yet')).toBeVisible();
    await shot(page, `empty-${theme}`);
    await runAxe(page, `empty-${theme}`, testInfo);
  });

  test(`health, history+diff, settings ${theme}`, async ({ page }, testInfo) => {
    await initStorage(page, { theme, live: false });
    await installMocks(page, { scenario: 'scan' });
    await page.goto('/');
    await scanToFinish(page);
    await page.locator('#tab-health').click();
    await expect(page.locator('.hcard').first()).toBeVisible();
    await shot(page, `health-${theme}`);
    await runAxe(page, `health-${theme}`, testInfo);

    await page.locator('#tab-history').click();
    await page.getByRole('button', { name: 'Compare', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Added (1)' })).toBeVisible();
    await shot(page, `history-${theme}`);
    await runAxe(page, `history-${theme}`, testInfo);

    await page.locator('#nav-settings').click();
    await expect(page.locator('#settings-dialog')).toBeVisible();
    await page.screenshot({ path: path.join(OUT_DIR, 'shots', `settings-${theme}.png`) });
    await runAxe(page, `settings-${theme}`, testInfo);
  });
}

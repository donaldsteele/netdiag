// Web server health card: the "set the URL" link when none is set.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage } from '../support/mock.mjs';

const webCard = (page) => page.locator('.hcard').filter({ has: page.getByRole('heading', { name: 'Web server' }) });

test('web server card links to Settings when no URL is set', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.goto('/');
  await page.locator('#tab-health').click();
  const card = webCard(page);
  await expect(card).toContainText('no web server configured');
  // Other cards never offer the link.
  await expect(page.locator('.hcard a.setweb:visible')).toHaveCount(1);
  const link = card.getByRole('link', { name: 'Set the web server URL in Settings' });
  await expect(link).toBeVisible();
  await link.click();
  const dialog = page.locator('#settings-dialog');
  await expect(dialog).toHaveJSProperty('open', true);
  await expect(dialog.getByLabel('Web server URL')).toBeVisible();
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

test('web server card hides the link once a URL is set', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { scenario: 'scan', web: 'ok' });
  await page.goto('/');
  await page.locator('#tab-health').click();
  const card = webCard(page);
  await expect(card).toContainText('HTTP 200');
  await expect(card.getByRole('link', { name: 'Set the web server URL in Settings' })).toBeHidden();
});

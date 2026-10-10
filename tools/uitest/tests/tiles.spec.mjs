// T4: stat tiles, health strip, progress bar.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage } from '../support/mock.mjs';
import { scanToFinish } from '../support/steps.mjs';
import * as fx from '../fixtures/index.mjs';

const stats = fx.expectedStats();

test('tiles show fixture counts and filter the table on click', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { scenario: 'scan' });
  await page.goto('/');
  await scanToFinish(page);
  const list = fx.hosts();
  const want = {
    alive: stats.alive, printers: stats.printers, web: stats.web,
    mdns: list.filter((h) => h.mdns).length,
    offline: list.filter((h) => h.alive && h.online === false && !h.isSelf).length,
  };
  for (const [id, n] of Object.entries(want)) {
    await expect(page.locator(`#tile-${id} .tv`)).toHaveText(String(n));
  }
  await expect(page.locator('#tile-conflicts .tv')).toHaveText('2');
  await expect(page.locator('#tile-conflicts')).toHaveClass(/tone-bad/);

  await page.locator('#tile-printers').click();
  await expect(page.locator('#tile-printers')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('#hosts tbody tr.host')).toHaveCount(stats.printers);
  await page.locator('#tile-printers').click();
  await expect(page.locator('#hosts tbody tr.host')).toHaveCount(fx.TOTAL_HOSTS);
  await page.locator('#tile-alive').click();
  await expect(page.locator('#hosts tbody tr.host')).toHaveCount(stats.alive);
});

test('health strip has 6 pills and opens the Health view', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { scenario: 'scan' });
  await page.goto('/');
  const pills = page.locator('.hstrip .hpill');
  await expect(pills).toHaveCount(6);
  await expect(page.locator('#hp-wifi')).toContainText('Warning');
  await expect(page.locator('#hp-dns')).toContainText('OK');
  await page.locator('#hp-dns').click();
  await expect(page.locator('#health')).toBeVisible();
});

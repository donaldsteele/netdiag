import { test, expect } from '@playwright/test';
import { installMocks, initStorage } from '../support/mock.mjs';

async function live(page) {
  await initStorage(page, { theme: 'dark', live: true });
  await installMocks(page, { scenario: 'live' });
  await page.goto('/');
  await page.getByRole('button', { name: 'Scan', exact: true }).click();
  await expect(page.locator('.livepill')).toContainText('Live');
  await expect(page.locator('#hosts tbody tr.host')).toHaveCount(254);
}

test('activity log lines, ticker, filters, copy, cap', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await live(page);
  await expect(page.locator('#ticker .tick')).toHaveCount(3);
  await page.locator('#ticker').click();
  await expect(page.locator('#tab-activity')).toHaveAttribute('aria-current', 'page');
  const log = page.getByRole('log', { name: 'Activity log' });
  await expect(log).toContainText('New device: 192.168.2.254');
  await expect(log).toContainText('Went offline: 192.168.2.200');
  await expect(log).toContainText('Conflict:');
  await expect(log).toContainText('Device offline');
  await expect(log).toContainText('Scan started');

  await page.locator('#lf-offline').click();
  await expect(page.locator('#lf-offline')).toHaveAttribute('aria-pressed', 'false');
  await expect(log).not.toContainText('Went offline: 192.168.2.200');
  await expect(log).toContainText('Conflict:');
  await page.locator('#lf-offline').click();

  await page.locator('#log-copy').click();
  const clip = await page.evaluate(() => navigator.clipboard.readText());
  expect(clip).toContain('HOST');
  expect(clip).toContain('Went offline: 192.168.2.200');

  const n = await page.evaluate(() => {
    const a = window.Alpine.$data(document.querySelector('.shell'));
    for (let i = 0; i < 600; i++) a.logPush('host', 'bulk ' + i);
    return a.log.length;
  });
  expect(n).toBe(500);
  await expect.poll(() => page.locator('#log .logline').count()).toBe(500);
  await expect(log).toContainText('bulk 599');
});

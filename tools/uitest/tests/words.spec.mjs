// The v0.6 words audit: one word for each severity everywhere (Urgent,
// Warning, Note, Fixed: the banners, the Problems right now card and
// Settings > Send), the header chip says of the checks what the card says,
// and one action has one name (Check again).
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME } from '../support/mock.mjs';
import { scanToFinish } from '../support/steps.mjs';
import * as fx from '../fixtures/index.mjs';

const banners = (page) => page.locator('#alerts div.banner:not(.more)');

// A printer alert, critical in the one severity map (D3).
const cover = {
  id: 'n-90', trigger: 'printerAlert', severity: 'critical', ip: '192.168.2.10', title: 'Printer alert',
  message: 'BRW001BA9123456 (192.168.2.10): Door or cover open', key: 'printerAlert|00:1B:A9:12:34:56|doorOpen|1a2b3c4d',
  at: new Date().toISOString(),
};
const coverItem = {
  key: cover.key, kind: 'printerAlert', severity: 'critical', title: 'Printer alert', detail: 'Door or cover open',
  device: { ip: cover.ip, name: 'BRW001BA9123456' },
};

test('banners and the card say Urgent, Warning and Note, as Settings > Send does', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { frames: fx.framesWith(fx.hosts(), [], [cover, fx.notice(2)]),
    problems: fx.problemsView({ open: [coverItem, ...fx.problemItems().slice(1, 2)] }) });
  await page.goto(HOME);
  await scanToFinish(page);
  await expect(banners(page).filter({ hasText: 'Urgent: Printer alert' })).toHaveCount(1);
  await expect(banners(page).filter({ hasText: 'Note: Device offline' })).toHaveCount(1);
  await expect(page.locator('#alerts')).not.toContainText('Critical:');
  await expect(page.locator('#alerts')).not.toContainText('Notice:');
  const words = await page.locator('#problems-now .pitem .psev').allTextContents();
  expect(words[0].trim()).toBe('Urgent');
  for (const w of words) expect(['Urgent', 'Warning', 'Note', 'Fixed']).toContain(w.trim());
  // Settings > Send names the same level: urgent problems.
  await expect.poll(() => page.evaluate(() => !!window.Alpine.$data(document.querySelector('.shell')).settings)).toBe(true);
  await page.evaluate(() => document.querySelector('#nav-settings').click());
  await expect(page.locator('#chat-level option[value="critical"]')).toHaveText('Only urgent problems');
  expect(rec.unmatched).toEqual([]);
});

test('the header chip says a warning check is a note, as the card does', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  // The default report: Wi-Fi warns (weak signal), the rest are fine.
  const rec = await installMocks(page, {});
  await page.goto(HOME);
  await scanToFinish(page);
  const chip = page.locator('.hdrtools .chipbtn');
  await expect(chip).toHaveText('Health: 1 note');
  await expect(chip).toHaveClass(/h-note/);
  await expect(chip.locator('use')).toHaveAttribute('href', 'icons.svg#i-info');
  // The card lists the Wi-Fi item as a note, never as a warning.
  await expect(page.locator('#problems-now .pitem', { hasText: 'Health check warning' }).locator('.psev')).toHaveText('Note');
  expect(rec.unmatched).toEqual([]);
});

test('a failed check makes the chip say failing, with the problem shape', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { health: 'all' });
  await page.goto(HOME);
  await scanToFinish(page);
  // The scan's own report has one note; running the checks again reads
  // the report where checks fail.
  await page.locator('#tab-health').click();
  await page.locator('#health-run').click();
  const chip = page.locator('.hdrtools .chipbtn');
  await expect(chip).toHaveText(/^Health: \d+ failing(, \d+ notes?)?$/);
  await expect(chip).not.toContainText('warn');
  await expect(chip).toHaveClass(/h-fail/);
  await expect(chip.locator('use')).toHaveAttribute('href', 'icons.svg#i-circle-x');
  expect(rec.unmatched).toEqual([]);
});

test('the Health view runs the checks again with Check again, the same name as the card button', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { health: 'all' });
  await page.goto(HOME);
  await scanToFinish(page);
  await page.locator('#tab-health').click();
  const panel = page.locator('#health');
  await expect(panel.locator('#health-run')).toHaveText('Check again');
  await expect(panel.getByRole('button', { name: /^Recheck/ })).toHaveCount(0);
  // Each card's own button runs the same action under the same name.
  await expect(panel.locator('.hcard .aacts button', { hasText: 'Check again' }).first()).toBeVisible();
  const runs = () => rec.requests.filter((r) => r.method === 'POST' && r.path === '/api/health/run').length;
  const before = runs();
  await panel.locator('#health-run').click();
  await expect.poll(runs).toBe(before + 1);
  await panel.locator('.hcard .aacts button', { hasText: 'Check again' }).first().click();
  await expect.poll(runs).toBe(before + 2);
  expect(rec.unmatched).toEqual([]);
});

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
  // So do the health strip and the Health panel, under the chip.
  await expect(page.locator('#hp-wifi')).toHaveClass(/hs-info/);
  await expect(page.locator('#hp-wifi')).toContainText('Note');
  await page.locator('#tab-health').click();
  await expect(page.locator('#health-h .sub')).toHaveText('Health: 1 note');
  const wifi = page.locator('#health .hcard', { hasText: 'Wi-Fi' });
  await expect(wifi.locator('.hs')).toHaveText('Note');
  await expect(wifi.locator('.hs')).toHaveClass(/hs-info/);
  await expect(wifi.locator('.hs use')).toHaveAttribute('href', 'icons.svg#i-info');
  await expect(page.locator('#health')).not.toContainText('Warning');
  expect(rec.unmatched).toEqual([]);
});

// One check, the same shape and word in the four places that show it: the
// header chip, the health strip, the Health panel and the Problems right
// now card (problems.Current decides; the page follows it).
async function expectCheckEverywhere(page, { id, label, chip, level, shape, mark, word, cardTitle, cardWord }) {
  const c = page.locator('.hdrtools .chipbtn');
  await expect(c).toHaveText(chip);
  await expect(c).toHaveClass(new RegExp('h-' + level));
  await expect(c.locator('use')).toHaveAttribute('href', 'icons.svg#i-' + shape);
  const item = page.locator('#problems-now .pitem', { hasText: cardTitle }).filter({ hasText: label });
  await expect(item.locator('.psev')).toHaveText(cardWord);
  await expect(item.locator('.psev use')).toHaveAttribute('href', 'icons.svg#i-' + shape);
  await expect(page.locator('#hp-' + id)).toHaveClass(new RegExp('hs-' + mark));
  await expect(page.locator('#hp-' + id + ' use').last()).toHaveAttribute('href', 'icons.svg#i-' + shape);
  await page.locator('#tab-health').click();
  await expect(page.locator('#health-h .sub')).toHaveText(chip);
  const card = page.locator('#health .hcard', { hasText: label });
  await expect(card).toHaveClass(new RegExp('hc-' + mark));
  await expect(card.locator('.hs')).toHaveText(word);
  await expect(card.locator('.hs use')).toHaveAttribute('href', 'icons.svg#i-' + shape);
}

test('a failed name check alone is a warning, not urgent, in the chip, the strip, the Health panel and the card', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { health: 'dns' });
  await page.goto(HOME);
  await expectCheckEverywhere(page, { id: 'dns', label: 'Name lookup (DNS)', chip: 'Health: 1 failing', level: 'warn', shape: 'triangle-alert',
    mark: 'warn', word: 'Failed', cardTitle: 'Health check failed', cardWord: 'Warning' });
  expect(rec.unmatched).toEqual([]);
});

test('a check that could not run is a warning in the chip too, never Health: OK', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  // No router is known for the adapter, so the router check could not run.
  const rec = await installMocks(page, { health: 'nogateway' });
  await page.goto(HOME);
  await expectCheckEverywhere(page, { id: 'gateway', label: 'Router', chip: 'Health: 1 could not run', level: 'warn', shape: 'triangle-alert',
    mark: 'warn', word: 'Could not run', cardTitle: 'Health check could not run', cardWord: 'Warning' });
  await expect(page.locator('#health-fine')).toBeHidden();
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

// The storm guard (v0.6 F9c): when this computer loses the network, live
// mode pauses its checks instead of reporting every device offline. The live
// pill says why (a triangle plus the words), one local banner explains it,
// and when this computer is on another network the banner offers Restart
// watching.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME } from '../support/mock.mjs';
import { runAxe } from '../support/axe.mjs';
import { THEMES } from '../support/config.mjs';
import * as fx from '../fixtures/index.mjs';

async function liveWith(page, frames, theme = 'dark') {
  await initStorage(page, { theme, live: true });
  const rec = await installMocks(page, { scenario: 'live', frames });
  await page.goto(HOME);
  await page.getByRole('button', { name: 'Scan', exact: true }).click();
  await expect(page.locator('.livepill')).toContainText('Live');
  return rec;
}

const held = (reason) => [...fx.liveFrames(), { type: 'live', data: fx.heldStatus(reason) }, { type: 'notice', data: fx.linkNotice(reason) }];
const banners = (page) => page.locator('#alerts div.banner:not(.more)');
const hhmm = (iso) => new Date(iso).toTimeString().slice(0, 5);

test('checks paused: the pill and one banner say this computer lost the network', async ({ page }) => {
  const rec = await liveWith(page, held('link'));
  const pill = page.locator('.livepill');
  await expect(pill).toHaveText(`Live · checks paused: this computer lost the network (since ${hhmm(fx.HELD_SINCE)})`);
  await expect(pill).toHaveClass(/held/);
  await expect(pill.locator('svg.ico use')).toHaveAttribute('href', 'icons.svg#i-triangle-alert');
  const b = banners(page).filter({ hasText: 'netdiag lost the network' });
  await expect(b).toHaveCount(1);
  await expect(b).toHaveClass(/sev-warn/);
  await expect(b.getByRole('button', { name: 'Restart watching' })).toBeHidden();
  await b.getByText('What to try').click();
  await expect(b).toContainText("Check this computer's network cable or Wi-Fi.");
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

test('the router going quiet reads as paused checks too', async ({ page }) => {
  await liveWith(page, [...fx.liveFrames(), { type: 'live', data: fx.heldStatus('gateway') }]);
  await expect(page.locator('.livepill')).toHaveText(`Live · checks paused: the router stopped answering (since ${hhmm(fx.HELD_SINCE)})`);
});

test('back on the network: the banner turns Fixed and the pill is the usual one', async ({ page }) => {
  const frames = [...held('link'), { type: 'notice', data: fx.linkBackNotice() },
    { type: 'live', data: { ...fx.heldStatus('link'), held: false, heldReason: '', heldSince: '', cycle: 5 } }];
  await liveWith(page, frames);
  const pill = page.locator('.livepill');
  await expect(pill).toContainText('Live · 0 new · 0 offline');
  await expect(pill).not.toHaveClass(/held/);
  await expect(pill.locator('svg.ico')).toHaveCount(0);
  const fixed = banners(page).filter({ hasText: 'Fixed: netdiag is back on the network' });
  await expect(fixed).toHaveCount(1);
  await expect(banners(page).filter({ hasText: 'netdiag lost the network' })).toHaveCount(0);
});

test('another network: alerts stopped, and the banner offers Restart watching (Esc returns focus)', async ({ page }) => {
  const rec = await liveWith(page, held('network_changed'));
  await expect(page.locator('.livepill')).toHaveText('Live · alerts stopped: this computer is on a different network now');
  const b = banners(page).filter({ hasText: 'This computer is on a different network' });
  await expect(b).toHaveCount(1);
  const btn = b.getByRole('button', { name: 'Restart watching' });
  await expect(btn).toBeVisible();
  await btn.click();
  const dlg = page.locator('#restart-dialog');
  await expect(dlg).toHaveJSProperty('open', true);
  await expect(dlg.locator('#restart-keep')).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(dlg).toHaveJSProperty('open', false);
  await expect(btn).toBeFocused();
  await btn.click();
  await dlg.getByRole('button', { name: 'Restart', exact: true }).click();
  // The first POST is the Scan click that started the watching.
  await expect.poll(() => rec.requests.filter((r) => r.method === 'POST' && r.path === '/api/scans').length).toBe(2);
  const post = rec.requests.filter((r) => r.method === 'POST' && r.path === '/api/scans')[1];
  expect(JSON.parse(post.body)).toMatchObject({ live: true, replaceLive: true });
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

test('lost the network, then another network: one banner, which now offers Restart watching', async ({ page }) => {
  const frames = [...held('link'), { type: 'live', data: fx.heldStatus('network_changed') },
    { type: 'notice', data: fx.linkNotice('network_changed') }];
  const rec = await liveWith(page, frames);
  await expect(page.locator('.livepill')).toHaveText('Live · alerts stopped: this computer is on a different network now');
  const b = banners(page).filter({ hasText: 'This computer is on a different network' });
  await expect(b).toHaveCount(1);
  await expect(banners(page).filter({ hasText: 'netdiag lost the network' })).toHaveCount(0);
  await expect(b.getByRole('button', { name: 'Restart watching' })).toBeVisible();
  await b.getByText('What to try').click();
  await expect(b).toContainText('press Restart watching to watch the network it is on now');
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

test("Restart watching from the banner watches the address range this computer has now", async ({ page }) => {
  const rec = await liveWith(page, held('network_changed'));
  // This computer moved: its Wi-Fi has another address range now.
  const moved = fx.interfaces().map((i) => i.name === 'Wi-Fi' ? { ...i, ip: '10.20.0.50', cidr: '10.20.0.0/24', suggested: '10.20.0.0/24' } : i);
  await page.route('**/api/interfaces', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(moved) }));
  const btn = banners(page).filter({ hasText: 'This computer is on a different network' }).getByRole('button', { name: 'Restart watching' });
  await btn.click();
  const dlg = page.locator('#restart-dialog');
  await expect(dlg).toHaveJSProperty('open', true);
  await expect(dlg.locator('#restart-settings')).toContainText('10.20.0.0/24');
  await expect(dlg.locator('#restart-keep')).toBeFocused();
  await dlg.getByRole('button', { name: 'Restart', exact: true }).click();
  await expect.poll(() => rec.requests.filter((r) => r.method === 'POST' && r.path === '/api/scans').length).toBe(2);
  const post = rec.requests.filter((r) => r.method === 'POST' && r.path === '/api/scans')[1];
  expect(JSON.parse(post.body)).toMatchObject({ cidr: '10.20.0.0/24', interface: 'Wi-Fi', live: true, replaceLive: true });
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

for (const theme of THEMES) {
  test(`held pill and banners are axe clean (${theme})`, async ({ page }, testInfo) => {
    // A new device's row fades in (rowin, 0.6 s): axe must not measure its
    // contrast halfway through.
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await liveWith(page, held('network_changed'), theme);
    await expect(banners(page).filter({ hasText: 'This computer is on a different network' })).toHaveCount(1);
    await runAxe(page, `held-${theme}`, testInfo);
  });
}

test('the held pill causes no horizontal scroll from 360 to 1440', async ({ page }) => {
  await liveWith(page, held('link'));
  const pill = page.locator('.livepill');
  for (const width of [360, 390, 768, 1100, 1440]) {
    await page.setViewportSize({ width, height: 800 });
    const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(over, `horizontal overflow at ${width}`).toBeLessThanOrEqual(0);
    const box = await pill.boundingBox();
    expect(box.x + box.width, `pill inside the page at ${width}`).toBeLessThanOrEqual(width + 0.5);
  }
});

// The storm guard (v0.6 F9c): when this computer loses the network, live
// mode pauses its checks instead of reporting every device offline. The live
// pill says why (a triangle plus the words), one local banner explains it,
// and when this computer is on another network the banner offers Restart
// watching.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME } from '../support/mock.mjs';
import { runAxe } from '../support/axe.mjs';
import { THEMES } from '../support/config.mjs';
import { cardClock } from '../support/steps.mjs';
import * as fx from '../fixtures/index.mjs';

async function liveWith(page, frames, theme = 'dark') {
  await initStorage(page, { theme, live: true });
  const rec = await installMocks(page, { scenario: 'live', frames });
  await page.goto(HOME);
  await page.getByRole('button', { name: 'Scan', exact: true }).click();
  // liveText() starts with "Live" before the stream arrives, while x-show
  // still hides the pill: wait until the pill is shown as well.
  await expect(page.locator('.livepill')).toBeVisible();
  await expect(page.locator('.livepill')).toContainText('Live');
  return rec;
}

const held = (reason) => [...fx.liveFrames(), { type: 'live', data: fx.heldStatus(reason) }, { type: 'notice', data: fx.linkNotice(reason) }];
const banners = (page) => page.locator('#alerts div.banner:not(.more)');
// The pill and the card give the day of a hold that began on another day.
const since = (iso) => cardClock(iso);

test('checks paused: the pill and one banner say this computer lost the network', async ({ page }) => {
  const rec = await liveWith(page, held('link'));
  const pill = page.locator('.livepill');
  await expect(pill).toHaveText(`Live · checks paused: this computer lost the network (since ${since(fx.HELD_SINCE)})`);
  await expect(pill).toHaveClass(/held/);
  await expect(pill.locator('svg.ico use')).toHaveAttribute('href', 'icons.svg#i-triangle-alert');
  const b = banners(page).filter({ hasText: 'This computer lost the network' });
  await expect(b).toHaveCount(1);
  await expect(b).toHaveClass(/sev-warn/);
  await expect(b.getByRole('button', { name: 'Restart watching' })).toBeHidden();
  // The banner says it in a few words; What to try says why, and when the
  // checks start again, instead of the same sentence twice.
  await expect(b.locator('.btext > span').first()).toHaveText('netdiag paused its checks.');
  await b.getByText('What to try').click();
  await expect(b.locator('.advice')).toContainText('They start again by themselves when the network is back.');
  await expect(b).toContainText("Check this computer's network cable or Wi-Fi.");
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

// The banner under the pill blames the router, as the pill does, never this
// computer or netdiag: someone reading it checks the router, not this
// computer's cable.
test('the router going quiet reads as paused checks too, and its banner blames the router', async ({ page }) => {
  await liveWith(page, held('gateway'));
  await expect(page.locator('.livepill')).toHaveText(`Live · checks paused: the router stopped answering (since ${since(fx.HELD_SINCE)})`);
  // The banner says what the pill says, not that this computer lost the network.
  const b = banners(page).filter({ hasText: 'The router stopped answering' });
  await expect(b).toHaveCount(1);
  await expect(b.locator('.btext > strong')).toHaveText('Warning: The router stopped answering');
  await expect(banners(page).filter({ hasText: 'lost the network' })).toHaveCount(0);
  await b.getByText('What to try').click();
  await expect(b).toContainText('Check that the router is switched on and its lights look normal.');
});

// The day of a hold that began on another day is in the pill as on the
// card, not only its time.
test('a hold from another day names its day in the pill', async ({ page }) => {
  await liveWith(page, held('link'));
  expect(since(fx.HELD_SINCE), 'the fixture hold began on another day').toMatch(/^(\d+ \w{3}|\w{3}) \d\d:\d\d$/);
  await expect(page.locator('.livepill')).toContainText(`(since ${since(fx.HELD_SINCE)})`);
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
  await expect(banners(page).filter({ hasText: 'This computer lost the network' })).toHaveCount(0);
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
  await expect(banners(page).filter({ hasText: 'This computer lost the network' })).toHaveCount(0);
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

// The top bar's Restart watching reads the interfaces again too while this
// computer is on another network: it never restarts on the range it left.
test('Restart watching in the top bar after a move watches the range this computer has now', async ({ page }) => {
  const rec = await liveWith(page, held('network_changed'));
  const moved = fx.interfaces().map((i) => i.name === 'Wi-Fi' ? { ...i, ip: '10.9.0.50', cidr: '10.9.0.0/24', suggested: '10.9.0.0/24' } : i);
  await page.route('**/api/interfaces', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(moved) }));
  const top = page.locator('#restart-btn');
  await top.click();
  const dlg = page.locator('#restart-dialog');
  await expect(dlg).toHaveJSProperty('open', true);
  // It names the range being watched, and the one it will watch now.
  await expect(dlg.locator('#restart-q')).toContainText(`netdiag is watching ${fx.CIDR}`);
  await expect(dlg.locator('#restart-settings')).toHaveText('These settings: 10.9.0.0/24, live mode.');
  await expect(dlg.locator('#restart-keep')).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(top).toBeFocused();
  await top.click();
  await dlg.getByRole('button', { name: 'Restart', exact: true }).click();
  await expect.poll(() => rec.requests.filter((r) => r.method === 'POST' && r.path === '/api/scans').length).toBe(2);
  const post = rec.requests.filter((r) => r.method === 'POST' && r.path === '/api/scans')[1];
  expect(JSON.parse(post.body)).toMatchObject({ cidr: '10.9.0.0/24', interface: 'Wi-Fi', live: true, replaceLive: true });
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

// Dismissing the banner (or a banner pushed under "N more") does not leave
// the person without a way out: the card's Stopped note has Restart
// watching too.
test('after the banner is dismissed, the card offers Restart watching', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: true });
  const rec = await installMocks(page, { scenario: 'live', frames: held('network_changed') });
  rec.problems = fx.problemsView({ source: 'live', watching: true, held: true, heldReason: 'network_changed', heldSince: fx.HELD_SINCE });
  await page.goto(HOME);
  await page.getByRole('button', { name: 'Scan', exact: true }).click();
  const b = banners(page).filter({ hasText: 'This computer is on a different network' });
  await expect(b).toHaveCount(1);
  await b.getByRole('button', { name: /^Dismiss/ }).click();
  await expect(b).toHaveCount(0);
  await page.evaluate(() => window.Alpine.$data(document.querySelector('.shell')).loadProblems());
  const note = page.locator('#pn-held');
  await expect(note).toContainText('Stopped This computer is on a different network now');
  const btn = note.getByRole('button', { name: 'Restart watching', exact: true });
  await expect(btn).toBeVisible();
  await btn.click();
  const dlg = page.locator('#restart-dialog');
  await expect(dlg).toHaveJSProperty('open', true);
  await page.keyboard.press('Escape');
  await expect(dlg).toHaveJSProperty('open', false);
  await expect(btn).toBeFocused();
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

// On a stale page the three Restart watching buttons (top bar, banner and
// card) look and are announced the same: off. netdiag refuses each.
test('on a stale page every Restart watching looks off', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: true });
  const rec = await installMocks(page, { scenario: 'live', frames: held('network_changed'), stale: true });
  rec.problems = fx.problemsView({ source: 'live', watching: true, held: true, heldReason: 'network_changed', heldSince: fx.HELD_SINCE });
  await page.goto(HOME);
  await page.getByRole('button', { name: 'Scan', exact: true }).click();
  const banner = banners(page).filter({ hasText: 'This computer is on a different network' });
  await expect(banner).toHaveCount(1);
  await expect(page.locator('#pn-restart')).toBeVisible();
  // netdiag was started again: this page's token is out of date now.
  rec.session = 'new-token';
  await page.evaluate(() => window.Alpine.$data(document.querySelector('.shell')).loadSettings());
  await expect(page.locator('#session-off')).toBeVisible();
  const all = [page.locator('#restart-btn'), banner.locator('.brestart'), page.locator('#pn-restart')];
  for (const b of all) {
    await expect(b).toHaveAttribute('aria-disabled', 'true');
    expect(await b.evaluate((el) => getComputedStyle(el).opacity)).toBe('0.55');
  }
  await banner.locator('.brestart').click({ force: true });
  await expect(page.locator('#restart-dialog')).toHaveJSProperty('open', true);
  await page.keyboard.press('Escape');
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

// Phones: the banners scroll away with the page (three pinned banners with
// What to try covered half of a 390 px screen), and the live pill has a row
// of its own, so its words wrap across the width instead of a tall narrow
// box (and the usual pill is not cut off).
test('on a phone the banners scroll with the page and the live pill has its own row', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await liveWith(page, [...held('network_changed'), { type: 'notice', data: fx.notice(1) }]);
  await expect(banners(page)).toHaveCount(3);
  const pill = page.locator('.livepill');
  const pb = await pill.boundingBox();
  const bar = await page.locator('.topbar').boundingBox();
  expect(pb.width, 'the pill spans the top bar').toBeGreaterThan(bar.width - 40);
  expect(pb.height, 'the pill wraps to at most 3 lines').toBeLessThan(80);
  await page.locator('#hosts').scrollIntoViewIfNeeded();
  await page.evaluate(() => window.scrollBy(0, 400));
  const first = await banners(page).first().boundingBox();
  expect(first.y + first.height, 'the first banner scrolled away').toBeLessThanOrEqual(0);
  // At 1100 px and up the banners stay at the top while the page scrolls.
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.evaluate(() => window.scrollBy(0, 600));
  const top = await banners(page).first().boundingBox();
  expect(top.y, 'pinned at the top on a wide screen').toBeGreaterThanOrEqual(-1);
  expect(top.y).toBeLessThan(5);
  // The usual pill shows its whole text on a phone too.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate((st) => { window.Alpine.$data(document.querySelector('.shell')).liveStatus = st; },
    { ...fx.heldStatus('link'), held: false, heldReason: '', heldSince: '' });
  await expect(pill).not.toHaveClass(/held/);
  expect(await pill.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the pill is not cut off').toBe(true);
  await expect(pill).toContainText('watching 1 addresses');
});

test('the held pill causes no horizontal scroll from 360 to 1440', async ({ page }) => {
  await liveWith(page, held('link'));
  const pill = page.locator('.livepill');
  await expect(pill).toHaveClass(/held/);
  for (const width of [360, 390, 768, 1100, 1440]) {
    await page.setViewportSize({ width, height: 800 });
    const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(over, `horizontal overflow at ${width}`).toBeLessThanOrEqual(0);
    const box = await pill.boundingBox();
    expect(box.x + box.width, `pill inside the page at ${width}`).toBeLessThanOrEqual(width + 0.5);
  }
});

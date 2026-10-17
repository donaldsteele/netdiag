// "Fixed" messages (v0.6 F2a): an OK notice closes the problem its key
// opened. The fault's banner turns into a Fixed banner in place (circle-check
// plus the word), with no sound; the desktop notification is replaced with
// the same tag, silently (D8); the Activity log gets a Fixed line under the
// Notice chip.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage } from '../support/mock.mjs';
import { runAxe } from '../support/axe.mjs';
import * as fx from '../fixtures/index.mjs';

// Counts oscillator starts (one per tone) and records desktop notifications.
async function spies(page) {
  await page.addInitScript(() => {
    window.__tones = 0;
    window.__notes = [];
    const param = { value: 0, setValueAtTime() {}, linearRampToValueAtTime() {}, exponentialRampToValueAtTime() {} };
    window.AudioContext = function () {
      return {
        currentTime: 0, destination: {},
        createOscillator: () => ({ type: '', frequency: { ...param }, connect() {}, start() { window.__tones++; }, stop() {} }),
        createGain: () => ({ gain: { ...param }, connect() {} }),
      };
    };
    window.Notification = class {
      constructor(title, opts) { window.__notes.push({ title, ...opts }); }
      static get permission() { return 'granted'; }
      static requestPermission() { return Promise.resolve('granted'); }
    };
  });
}

function loudSettings() {
  const s = fx.settings();
  s.alerts.sound = true;
  s.alerts.desktop = true;
  return s;
}

async function liveWith(page, frames, theme = 'dark') {
  await spies(page);
  await initStorage(page, { theme, live: true });
  const rec = await installMocks(page, { scenario: 'live', settings: loudSettings(), frames });
  await page.goto('/');
  await page.getByRole('button', { name: 'Scan', exact: true }).click();
  await expect(page.locator('.livepill')).toContainText('Live');
  return rec;
}

const banners = (page) => page.locator('#alerts div.banner:not(.more)');

test('an OK notice flips its banner to Fixed in place, silently', async ({ page }) => {
  const rec = await liveWith(page, [...fx.liveFrames(), { type: 'notice', data: fx.fixedNotice(2) }]);
  const fixed = banners(page).filter({ hasText: 'Fixed:' });
  await expect(fixed).toHaveCount(1);
  await expect(banners(page)).toHaveCount(1); // the fault's banner became the Fixed one
  await expect(fixed).toHaveClass(/sev-ok/);
  await expect(fixed.locator('strong')).toHaveText('Fixed: Device back online');
  await expect(fixed).toContainText(/at \d\d:\d\d \(was down 12 min\)\./);
  await expect(fixed).toContainText('192.168.2.200');
  // Shape plus word: the circle-check glyph, not the warning triangle.
  await expect(fixed.locator('svg.ico use').first()).toHaveAttribute('href', 'icons.svg#i-circle-check');
  await expect(fixed).toHaveAttribute('role', 'status');

  // The fault beeped once (one warn tone); the OK made no sound.
  expect(await page.evaluate(() => window.__tones)).toBe(1);
  // The fault's desktop notification is replaced: same tag, silent.
  const notes = await page.evaluate(() => window.__notes);
  expect(notes).toHaveLength(2);
  expect(notes[0]).toMatchObject({ title: 'Device offline', tag: fx.notice(2).key });
  expect(notes[0].silent).toBeFalsy();
  expect(notes[1]).toMatchObject({ title: 'Fixed: Device back online', tag: fx.notice(2).key, silent: true });
  expect(notes[1].body).toMatch(/^Fixed at \d\d:\d\d \(was down 12 min\)\.$/);

  // Activity: the Fixed line sits under the Notice chip; still 8 chips.
  await page.locator('#tab-activity').click();
  const log = page.getByRole('log', { name: 'Activity log' });
  await expect(log).toContainText('Fixed: Device back online at');
  await expect(log).toContainText('(was down 12 min).');
  await expect(page.locator('[id^="lf-"]')).toHaveCount(8);
  await page.locator('#lf-notice').click();
  await expect(log).not.toContainText('Fixed: Device back online');
  await page.locator('#lf-notice').click();
  await expect(log).toContainText('Fixed: Device back online');
  expect(rec.unmatched).toEqual([]);
  expect(rec.external).toEqual([]);
});

test('an OK for a dismissed or unseen banner adds a compact Fixed banner', async ({ page }) => {
  // fixedNotice(1) closes a problem whose banner this page never showed.
  await liveWith(page, [...fx.liveFrames(), { type: 'notice', data: fx.fixedNotice(1) }]);
  await expect(banners(page)).toHaveCount(2);
  const fixed = banners(page).filter({ hasText: 'Fixed: Supply OK again' });
  await expect(fixed).toHaveCount(1);
  await expect(fixed).toContainText('(was down 12 min).');
  await expect(banners(page).filter({ hasText: 'Warning: Device offline' })).toHaveCount(1);

  // Dismiss the fault, then its OK arrives: a new Fixed banner, still no beep.
  await banners(page).filter({ hasText: 'Warning: Device offline' }).getByRole('button', { name: /Dismiss/ }).click();
  await expect(banners(page)).toHaveCount(1);
  const tones = await page.evaluate(() => window.__tones);
  await page.evaluate((n) => {
    const a = window.Alpine.$data(document.querySelector('.shell'));
    a.onNotice(n, a.scanId);
  }, fx.fixedNotice(2));
  await expect(banners(page).filter({ hasText: 'Fixed: Device back online' })).toHaveCount(1);
  expect(await page.evaluate(() => window.__tones)).toBe(tones);
});

for (const theme of ['dark', 'light']) {
  test(`Fixed banner is axe clean (${theme})`, async ({ page }, testInfo) => {
    await liveWith(page, [...fx.liveFrames(), { type: 'notice', data: fx.fixedNotice(2) }, { type: 'notice', data: fx.notice(1) }], theme);
    await expect(banners(page).filter({ hasText: 'Fixed:' })).toHaveCount(1);
    await expect(banners(page)).toHaveCount(2);
    await runAxe(page, `fixed-banner-${theme}`, testInfo);
  });
}

test('Fixed banner causes no horizontal scroll from 360 to 1440', async ({ page }) => {
  await liveWith(page, [...fx.liveFrames(), { type: 'notice', data: fx.fixedNotice(2) }, { type: 'notice', data: fx.notice(1) }]);
  const fixed = banners(page).filter({ hasText: 'Fixed:' });
  await expect(fixed).toHaveCount(1);
  for (const width of [360, 390, 768, 1100, 1440]) {
    await page.setViewportSize({ width, height: 800 });
    const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(over, `horizontal overflow at ${width}`).toBeLessThanOrEqual(0);
    const box = await fixed.boundingBox();
    expect(box.x + box.width, `banner inside the page at ${width}`).toBeLessThanOrEqual(width + 0.5);
  }
});

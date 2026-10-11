// One vertical scroller: the page. No view may nest a vertically scrolling box (the devices
// table, the activity log, history tables), at any width. The drawer and the settings dialog
// are overlays and scroll on their own. From 1100px the device table header sticks to the page
// just under the alert banners.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage } from '../support/mock.mjs';
import { scanToFinish } from '../support/steps.mjs';

const nestedScrollers = (page) => page.evaluate(() => [...document.querySelectorAll('main *')].filter(el => {
  const s = getComputedStyle(el), r = el.getBoundingClientRect();
  return r.height > 0 && !el.closest('#drawer, dialog') && ['auto', 'scroll'].includes(s.overflowY) && el.scrollHeight > el.clientHeight + 1;
}).map(el => (el.id ? '#' + el.id : el.tagName.toLowerCase() + '.' + el.className) + ' ' + el.clientHeight + '/' + el.scrollHeight));

for (const width of [390, 768, 1100, 1440]) {
  test(`no nested vertical scrollers at ${width}px`, async ({ page }) => {
    await initStorage(page, { theme: 'dark', live: false });
    await installMocks(page, { scenario: 'scan' });
    await page.setViewportSize({ width, height: 800 });
    await page.goto('/');
    await scanToFinish(page);
    expect(await nestedScrollers(page), 'devices').toEqual([]);

    if (width >= 1100) {
      await page.mouse.wheel(0, 3000);
      await expect.poll(() => page.evaluate(() => {
        const th = document.querySelector('#hosts thead th').getBoundingClientRect().top;
        return Math.round(th) === document.getElementById('alerts').offsetHeight;
      }), 'header sticks under banners').toBe(true);
    }

    for (const tab of ['health', 'activity', 'history']) {
      await page.evaluate((t) => document.querySelector('#tab-' + t).click(), tab);
      await expect(page.locator('#tab-' + tab)).toHaveAttribute('aria-current', 'page');
      if (tab === 'activity') {
        await page.evaluate(() => {
          const a = window.Alpine.$data(document.querySelector('.shell'));
          for (let i = 0; i < 500; i++) a.logPush('host', 'bulk ' + i);
        });
        await expect(page.locator('#log .logline')).toHaveCount(500);
        // Following the newest line scrolls the page itself to the bottom.
        await expect.poll(() => page.evaluate(() => {
          const d = document.documentElement;
          return d.scrollTop + d.clientHeight >= d.scrollHeight - 8;
        })).toBe(true);
      }
      if (tab === 'history') {
        await page.locator('#history').getByRole('button', { name: 'Compare', exact: true }).click();
        await expect(page.getByRole('heading', { name: 'Added (1)' })).toBeVisible();
      }
      expect(await nestedScrollers(page), tab).toEqual([]);
    }
  });
}

// T8: motion is opt-in. Every CSS animation/transition lives inside
// @media (prefers-reduced-motion: no-preference), so reduced-motion emulation
// must leave nothing running, and the default (no-preference) must keep the motion.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage } from '../support/mock.mjs';

// Animated elements the spec checks by name. Each one must show a running animation
// by default and none under reduced motion.
const ANIMATED = [
  { name: 'LIVE pill pulse', sel: '.livepill' },
  { name: 'activity cursor blink', sel: '#activity .cursor' },
  { name: 'drawer slide-in', sel: '#drawer' },
  { name: 'new-host row fade-in', sel: '#hosts tbody tr.host.isnew' },
];

async function openAnimatedState(page) {
  await initStorage(page, { theme: 'dark', live: true });
  await installMocks(page, { scenario: 'live' });
  await page.goto('/');
  await page.getByRole('button', { name: 'Scan', exact: true }).click();
  await expect(page.locator('.livepill')).toContainText('Live');
  await expect(page.locator('#hosts tr.host.isnew')).toHaveCount(1);
  await page.locator('#hosts button.rowbtn[data-ip="192.168.2.10"]').click();
  await expect(page.locator('#drawer')).toBeVisible();
  await page.locator('#tab-activity').click();
  await expect(page.locator('#log .logline').first()).toBeAttached();
}

async function runningAnimations(page, sel) {
  return page.evaluate((s) => {
    const el = document.querySelector(s);
    if (!el) return null;
    return getComputedStyle(el).animationName;
  }, sel);
}

test('reduced motion: no CSS animation runs and no transition is declared', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await openAnimatedState(page);
  for (const a of ANIMATED) {
    const name = await runningAnimations(page, a.sel);
    expect(name, a.name + ' (' + a.sel + ')').toBe('none');
  }
  // Nothing at all may be animating: this also catches any rule added later without the media guard.
  const running = await page.evaluate(() => document.getAnimations().map(x => x.animationName || x.transitionProperty));
  expect(running, 'running animations or transitions under reduced motion').toEqual([]);
  // The tile tween and the progress fill must not transition either.
  const fill = await page.evaluate(() => getComputedStyle(document.querySelector('.fill')).transitionDuration);
  expect(fill).toBe('0s');
});

test('default motion: the animations exist and are gated by no-preference', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  await openAnimatedState(page);
  for (const a of ANIMATED) {
    const name = await runningAnimations(page, a.sel);
    expect(name, a.name + ' (' + a.sel + ')').not.toBe('none');
  }
  const fill = await page.evaluate(() => getComputedStyle(document.querySelector('.fill')).transitionDuration);
  expect(fill).not.toBe('0s');
});

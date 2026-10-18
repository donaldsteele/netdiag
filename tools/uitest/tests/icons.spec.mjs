// T5: icons everywhere, device-type display, accessible names preserved.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME } from '../support/mock.mjs';
import { scanToFinish } from '../support/steps.mjs';

const TYPES = ['gateway', 'printer', 'computer', 'phone', 'tv', 'nas', 'camera', 'network', 'iot', 'server', 'self', 'unknown'];
const HREF = /^icons\.svg#i-[a-z0-9-]+$/;

async function setup(page) {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.goto(HOME);
  await scanToFinish(page);
  return rec;
}

// Every matched, visible element must contain at least one svg use with an icons.svg#i- href.
async function expectIcons(loc, min = 1) {
  const n = await loc.count();
  expect(n).toBeGreaterThanOrEqual(min);
  for (let i = 0; i < n; i++) {
    const hrefs = await loc.nth(i).locator('svg use').evaluateAll((els) => els.map((e) => e.getAttribute('href')));
    expect(hrefs.length, `element ${i} has an icon`).toBeGreaterThan(0);
    for (const h of hrefs) expect(h).toMatch(HREF);
  }
}

test('status, badge, tile, health pill and nav elements carry sprite icons', async ({ page }) => {
  await setup(page);
  await expectIcons(page.locator('#hosts tbody tr.host').first().locator('th'));
  await expectIcons(page.locator('.badge.conflict:visible, .badge.cnote:visible'));
  await expectIcons(page.locator('.badge.printer:visible'));
  await expectIcons(page.locator('.tile'), 6);
  await expectIcons(page.locator('.hpill'), 6);
  await expectIcons(page.locator('.navitem'), 5);
  await expectIcons(page.locator('.chipbtn[class*=" h-"], .hdrtools .chipbtn'));
  // Status shape differs by state (not colour alone).
  const shapes = await page.locator('#hosts tbody tr.host th svg.sico use').evaluateAll((els) => [...new Set(els.map((e) => e.getAttribute('href')))]);
  expect(shapes).toContain('icons.svg#i-circle-check');
  expect(shapes).toContain('icons.svg#i-circle-help');
});

test('nav buttons keep accessible names', async ({ page }) => {
  await setup(page);
  for (const name of ['Devices', 'Health', 'Activity', 'History', 'Settings']) {
    await expect(page.getByRole('button', { name, exact: true })).toBeVisible();
  }
});

test('health cards and activity log lines carry icons', async ({ page }) => {
  await setup(page);
  await page.getByRole('button', { name: 'Health', exact: true }).click();
  await expectIcons(page.locator('.hcard'), 1);
  await page.getByRole('button', { name: 'Activity', exact: true }).click();
  await expectIcons(page.locator('#log .logline'), 1);
  await expectIcons(page.locator('.logchip'), 8);
});

test('every device type renders its type icon and drawer shows label + reason', async ({ page }) => {
  await setup(page);
  const seen = await page.locator('#hosts tbody tr.host .typecell use').evaluateAll((els) => [...new Set(els.map((e) => e.getAttribute('href')))]);
  for (const t of TYPES) expect(seen, `type icon ${t}`).toContain('icons.svg#i-type-' + t);

  const cases = [['192.168.2.1', 'Gateway', 'gateway', 'default gateway'], ['192.168.2.10', 'Printer', 'printer', 'hrDeviceType=printer'],
    ['192.168.2.30', 'NAS/storage', 'nas', 'ReadyNAS']];
  for (const [ip, label, t, reason] of cases) {
    await page.locator(`#hosts button.rowbtn[data-ip="${ip}"]`).click();
    const row = page.locator('#drawer-type');
    await expect(row).toContainText(label);
    await expect(row.locator('.typereason')).toContainText(reason);
    await expect(row.locator('use')).toHaveAttribute('href', 'icons.svg#i-type-' + t);
    await page.locator('#drawer').getByRole('button', { name: 'Close details' }).click();
  }
});

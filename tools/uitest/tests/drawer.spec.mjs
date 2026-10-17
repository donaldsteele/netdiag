// T5: device table search + detail drawer (keyboard, focus, live update, perf).
import { test, expect } from '@playwright/test';
import { installMocks, initStorage } from '../support/mock.mjs';
import { scanToFinish } from '../support/steps.mjs';
import { runAxe } from '../support/axe.mjs';

async function setup(page) {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.goto('/');
  await scanToFinish(page);
  return rec;
}

test('drawer opens with Enter, Esc closes and returns focus to the row button', async ({ page }) => {
  const rec = await setup(page);
  const btn = page.locator('#hosts button.rowbtn[data-ip="192.168.2.10"]');
  await btn.focus();
  await page.keyboard.press('Enter');
  const drawer = page.locator('#drawer');
  await expect(drawer).toBeVisible();
  await expect(drawer.getByRole('heading', { name: '192.168.2.10' })).toBeVisible();
  await expect(drawer.getByRole('button', { name: 'Close details' })).toBeFocused();
  await expect(btn).toHaveAttribute('aria-expanded', 'true');
  await page.keyboard.press('Escape');
  await expect(drawer).toBeHidden();
  await expect(btn).toBeFocused();
  // Close button also works, and table sibling stays rendered (254 rows).
  await btn.click();
  await expect(drawer).toBeVisible();
  await drawer.getByRole('button', { name: 'Close details' }).click();
  await expect(drawer).toBeHidden();
  await expect(page.locator('#hosts tbody tr.host')).toHaveCount(254);
  expect(rec.unmatched).toEqual([]);
});

test('search filters by IP, vendor and MAC; printer drawer shows supplies', async ({ page }) => {
  await setup(page);
  const search = page.getByLabel('Search hosts by IP, name, vendor or MAC');
  await search.fill('192.168.2.10');
  const rows = page.locator('#hosts tbody tr.host');
  const n = await rows.count();
  expect(n).toBeGreaterThan(0);
  expect(n).toBeLessThan(254);
  await search.fill('zzz-no-such-host');
  await expect(rows).toHaveCount(0);
  await expect(page.getByText('No hosts match this filter.')).toBeVisible();
  await search.fill('');
  await expect(rows).toHaveCount(254);
  await page.locator('#tile-printers').click();
  await rows.first().locator('button.rowbtn').click();
  await expect(page.locator('#drawer .supplies li').first()).toBeVisible();
  await expect(page.locator('#drawer').getByRole('button', { name: 'Copy IP address' })).toBeVisible();
});

test('drawer updates in place when the host goes offline and never closes itself', async ({ page }) => {
  await setup(page);
  const ip = '192.168.2.10';
  await page.locator(`#hosts button.rowbtn[data-ip="${ip}"]`).click();
  const drawer = page.locator('#drawer');
  await expect(drawer).toBeVisible();
  await expect(drawer.locator('.badge.offline')).toBeHidden();
  await page.evaluate((ip) => {
    const a = window.Alpine.$data(document.querySelector('.shell'));
    const h = a.hosts.find((x) => x.ip === ip);
    a.upsert({ ...h, alive: true, online: false, offlineSince: new Date().toISOString() });
  }, ip);
  await expect(drawer.locator('.badge.offline')).toBeVisible();
  await expect(drawer).toBeVisible();
  await expect(page.locator(`#hosts tr.host:has(button[data-ip="${ip}"]) .badge.offline`)).toBeVisible();
});

test('254 hosts plus 100 live updates produce no long task over 200 ms', async ({ page }) => {
  await setup(page);
  await page.evaluate(() => {
    window.__long = [];
    new PerformanceObserver((l) => l.getEntries().forEach((e) => window.__long.push(e.duration)))
      .observe({ entryTypes: ['longtask'] });
  });
  await page.locator('#hosts button.rowbtn[data-ip="192.168.2.10"]').click();
  await page.evaluate(async () => {
    const a = window.Alpine.$data(document.querySelector('.shell'));
    for (let i = 0; i < 100; i++) {
      const h = a.hosts[i % a.hosts.length];
      a.upsert({ ...h, rttMs: i + 1, online: i % 2 === 0 });
      await new Promise((r) => setTimeout(r, 10));
    }
  });
  await page.waitForTimeout(300);
  const long = await page.evaluate(() => window.__long);
  expect(Math.max(0, ...long), `long tasks: ${long}`).toBeLessThan(200);
});

const RDP_HOST = '192.168.2.4';
const remotePosts = (rec) => rec.requests.filter((r) => r.path.endsWith('/remote'));

test('remote access: one click on the table link opens remote desktop, repeat clicks are ignored', async ({ page }) => {
  const rec = await setup(page);
  const row = page.locator(`#hosts tr.host:has(button.rowbtn[data-ip="${RDP_HOST}"])`);
  // This computer can open Remote Desktop but has no VNC viewer: only RDP is a link.
  const link = row.getByRole('link', { name: 'Remote desktop' });
  await expect(link).toBeVisible();
  await expect(row.getByRole('link', { name: 'Screen sharing (VNC)' })).toHaveCount(0);
  await link.click();
  await expect(row.getByText('Opening: look for its window on the taskbar')).toBeVisible();
  await link.click(); // a second click while it starts must not open a second session
  await page.waitForTimeout(300);
  expect(remotePosts(rec).map((r) => JSON.parse(r.body))).toEqual([{ ip: RDP_HOST, kind: 'rdp' }]);
  await expect(page.locator('#drawer')).toBeHidden(); // success does not open the drawer
  // With no drawer showing, screen readers hear the result through the page live region.
  await expect(page.locator('div.sr[role="status"]')).toContainText('Remote desktop to 192.168.2.4 is opening');
  // A device without remote access ports gets no link.
  await expect(page.locator('#hosts tr.host:has(button.rowbtn[data-ip="192.168.2.1"])').getByRole('link', { name: 'Remote desktop' })).toHaveCount(0);
  expect(rec.unmatched).toEqual([]);
});

test('remote access: drawer lists remote desktop, screen sharing and SSH with the right buttons', async ({ page }, testInfo) => {
  const rec = await setup(page);
  await page.locator(`#hosts button.rowbtn[data-ip="${RDP_HOST}"]`).click();
  const sec = page.locator('#drawer-remote');
  await expect(sec.getByRole('heading', { name: 'Remote access' })).toBeVisible();
  await expect(sec.getByText('Opens Remote Desktop Connection.', { exact: false })).toBeVisible();
  await expect(sec.getByRole('status')).toHaveCount(0);
  await sec.getByRole('button', { name: 'Open remote desktop' }).click();
  await expect.poll(() => remotePosts(rec).length).toBe(1);
  expect(JSON.parse(remotePosts(rec)[0].body)).toEqual({ ip: RDP_HOST, kind: 'rdp' });
  await expect(sec.getByRole('status')).toContainText('Remote desktop to 192.168.2.4 is opening');
  // The drawer's own status line says it; the page live region does not repeat it.
  await expect(page.locator('div.sr[role="status"]')).not.toContainText('Remote desktop to');
  await expect(sec.getByRole('button', { name: 'Open screen sharing' })).toBeHidden();
  await expect(sec.getByText("netdiag can't open screen sharing on this computer.", { exact: false })).toBeVisible();
  await expect(sec.getByRole('button', { name: 'Copy SSH command' })).toBeVisible();
  await expect(sec.getByRole('link', { name: 'Download connection file' })).toHaveAttribute('href', /\/remote\.rdp\?ip=192\.168\.2\.4$/);
  await runAxe(page, 'drawer-remote', testInfo);
  expect(rec.unmatched).toEqual([]);
});

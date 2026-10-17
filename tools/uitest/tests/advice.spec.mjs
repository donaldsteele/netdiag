// What this means / What to try (v0.6 M7): health card advice with the raw
// detail under Technical details, the banner disclosure and Show device, the
// honest conflict view, the printer-without-status block, the Location
// settings button and the "This network has no internet" switch (D15).
import { test, expect } from '@playwright/test';
import { installMocks, initStorage } from '../support/mock.mjs';
import { runAxe } from '../support/axe.mjs';
import { scanToFinish } from '../support/steps.mjs';
import { THEMES } from '../support/config.mjs';
import * as fx from '../fixtures/index.mjs';

const card = (page, label) => page.locator('.hcard').filter({ has: page.getByRole('heading', { name: label, exact: true }) });
const posts = (rec, path) => rec.requests.filter((r) => r.method === 'POST' && r.path === path);

const framesWith = fx.framesWith;

// Pretend the browser runs on platform (the Location button needs Windows).
async function onPlatform(page, platform) {
  await page.addInitScript((p) => {
    Object.defineProperty(Navigator.prototype, 'platform', { get: () => p });
    Object.defineProperty(Navigator.prototype, 'userAgentData', { get: () => undefined });
  }, platform);
}

for (const theme of THEMES) {
  test(`each of the 6 checks says what it means (${theme})`, async ({ page }, testInfo) => {
    await initStorage(page, { theme, live: false });
    const rec = await installMocks(page, { health: 'all' });
    await page.goto('/');
    await page.locator('#tab-health').click();
    const cards = page.locator('.hcard');
    await expect(cards).toHaveCount(6);
    for (let i = 0; i < 6; i++) await expect(cards.nth(i).locator('.advice .meaning')).toContainText('What this means:');
    const meanings = {
      'Gateway reachable': "This computer can't reach your router.",
      'DHCP lease': "This computer didn't get an address from the router",
      'DNS resolving': "Your local network works, but internet names can't be looked up.",
      'Web server': "The web server isn't answering as expected (HTTP 503).",
      'Internet reachability': "netdiag couldn't reach the internet.",
      'Wi-Fi': 'The Wi-Fi signal is weak (32%).',
    };
    for (const [label, m] of Object.entries(meanings)) await expect(card(page, label)).toContainText(m);
    await expect(page.locator('#health-fine')).toBeHidden();
    await runAxe(page, `advice-health-${theme}`, testInfo);

    // The raw detail waits under Technical details; Esc closes it again.
    const gw = card(page, 'Gateway reachable');
    const tech = gw.locator('details.tech');
    const raw = tech.locator('p.hdetail');
    await expect(raw).toBeHidden();
    await tech.locator('summary').click();
    await expect(raw).toBeVisible();
    await expect(raw).toHaveText(`${fx.GATEWAY} did not reply to ping or on ports 80 and 443`);
    await page.keyboard.press('Escape');
    await expect(raw).toBeHidden();
    await expect(tech.locator('summary')).toBeFocused();
    await expect(gw.locator('ol.steps li')).toHaveCount(3);

    // Check again runs the checks; Open Settings goes to the web server
    // address, and Esc returns to the button.
    const before = posts(rec, '/api/health/run').length;
    await gw.getByRole('button', { name: /^Check again/ }).click();
    await expect.poll(() => posts(rec, '/api/health/run').length).toBe(before + 1);
    const open = card(page, 'Web server').getByRole('button', { name: /^Open Settings/ });
    await open.click();
    await expect(page.locator('#settings-dialog')).toHaveJSProperty('open', true);
    await expect(page.locator('#set-weburl')).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(page.locator('#settings-dialog')).toHaveJSProperty('open', false);
    await expect(open).toBeFocused();
    // The old link gives way to the advice's Open Settings.
    await expect(page.locator('.hcard a.setweb:visible')).toHaveCount(0);
    expect(rec.unmatched, 'unmatched requests').toEqual([]);
    expect(rec.external, 'external requests').toEqual([]);
  });
}

test('"This network has no internet" makes Everything looks fine reachable', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { health: 'offline' });
  await page.goto('/');
  await page.locator('#tab-health').click();
  const dns = card(page, 'DNS resolving');
  await expect(dns.locator('.hs')).toContainText('Warning');
  await expect(dns).toContainText('If this network has no internet on purpose, turn on "This network has no internet" in Settings.');
  await expect(page.locator('#health-fine')).toBeHidden();

  await page.locator('#nav-settings').click();
  const dialog = page.locator('#settings-dialog');
  const sw = dialog.getByRole('switch', { name: 'This network has no internet' });
  await expect(sw).not.toBeChecked();
  await sw.check();
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(dialog).toHaveJSProperty('open', false);
  const puts = rec.requests.filter((r) => r.method === 'PUT' && r.path === '/api/settings');
  expect(puts).toHaveLength(1);
  const doc = JSON.parse(puts[0].body);
  expect(doc.noInternet).toBe(true);
  expect(doc.alerts).toEqual(fx.settings().alerts);
  // The switch changes what the name and internet checks say: check again.
  await expect.poll(() => posts(rec, '/api/health/run').length).toBe(1);
  const fine = page.locator('#health-fine');
  await expect(fine).toBeVisible();
  await expect(fine).toContainText('OK');
  await expect(fine).toContainText(/Everything looks fine \(checked \d\d:\d\d\)/);
  await expect(fine.locator('use')).toHaveAttribute('href', 'icons.svg#i-circle-check');
  await expect(dns.locator('.hs')).toContainText('Info');
  await expect(dns).toContainText('This network is set up without internet');
  await expect(card(page, 'Internet reachability')).toContainText('This network is set up without internet, so this check is only for information.');
  expect(rec.unmatched).toEqual([]);
});

test('a no-internet network with the switch on renders Everything looks fine', async ({ page }) => {
  await initStorage(page, { theme: 'light', live: false });
  await installMocks(page, { health: 'offline', settings: { ...fx.settings(), noInternet: true } });
  await page.goto('/');
  await page.locator('#tab-health').click();
  await expect(page.locator('#health-fine')).toContainText(/OK Everything looks fine \(checked \d\d:\d\d\)/);
  await expect(page.locator('.hcard .hs', { hasText: /Warning|Failed/ })).toHaveCount(0);
});

for (const [alt, heading] of [[0, 'Possible conflict'], [1, 'Possible conflict'], [2, 'Conflict']]) {
  test(`mac_flap with ${alt} alternation(s) reads "${heading}"`, async ({ page }) => {
    await initStorage(page, { theme: 'dark', live: false });
    const rec = await installMocks(page, { frames: framesWith(fx.flapHosts(alt), [fx.flapConflict(alt)]) });
    await page.goto('/');
    await scanToFinish(page);
    const btn = page.locator(`#hosts button.rowbtn[data-ip="${fx.FLAP_IP}"]`);
    await btn.click();
    const box = page.locator('#drawer .cbox');
    await expect(box).toHaveCount(1);
    await expect(box.locator('.chead strong')).toHaveText(heading);
    await expect(box.locator('.chead use')).toHaveAttribute('href', alt >= 2 ? 'icons.svg#i-circle-x' : 'icons.svg#i-triangle-alert');
    const cards = box.locator('.cpair .ccard');
    await expect(cards).toHaveCount(2);
    await expect(cards.nth(0)).toContainText(alt % 2 ? 'Device A:' : 'Device A: Espressif Inc.');
    await expect(cards.nth(0)).toContainText(fx.FLAP_MACS[0]);
    await expect(cards.nth(1)).toContainText(`Device B: Hewlett Packard · ${fx.FLAP_MACS[1]} · also uses 192.168.2.45${alt >= 1 ? ' · seen at ' : ''}`);
    await expect(box.locator('.advice')).toContainText(alt >= 2 ? 'Two devices are taking turns using 192.168.2.44' : 'Possible conflict: the device answering at 192.168.2.44 changed.');
    await expect(box.locator('.advice ol.steps li')).toHaveCount(alt >= 2 ? 3 : 2);
    await page.keyboard.press('Escape');
    await expect(page.locator('#drawer')).toBeHidden();
    await expect(btn).toBeFocused();
    expect(rec.unmatched).toEqual([]);
  });
}

test('the conflict pair stacks at 360 px and sits side by side when wide', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { frames: framesWith(fx.flapHosts(2), [fx.flapConflict(2)]) });
  await page.goto('/');
  await scanToFinish(page);
  await page.locator(`#hosts button.rowbtn[data-ip="${fx.FLAP_IP}"]`).click();
  const cards = page.locator('#drawer .cpair .ccard');
  await expect(cards).toHaveCount(2);
  let [a, b] = [await cards.nth(0).boundingBox(), await cards.nth(1).boundingBox()];
  expect(Math.abs(a.y - b.y), 'side by side at 1440').toBeLessThan(1);
  for (const width of [360, 390]) {
    await page.setViewportSize({ width, height: 800 });
    [a, b] = [await cards.nth(0).boundingBox(), await cards.nth(1).boundingBox()];
    expect(b.y, `stacked at ${width}`).toBeGreaterThanOrEqual(a.y + a.height - 1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), `no page scroll at ${width}`).toBeLessThanOrEqual(1);
  }
});

for (const theme of THEMES) {
  test(`conflict drawer with advice is axe clean (${theme})`, async ({ page }, testInfo) => {
    await initStorage(page, { theme, live: false });
    await installMocks(page, { frames: framesWith(fx.flapHosts(2), [fx.flapConflict(2)]) });
    await page.goto('/');
    await scanToFinish(page);
    await page.locator(`#hosts button.rowbtn[data-ip="${fx.FLAP_IP}"]`).click();
    await page.locator('#drawer .cbox details.tech summary').click();
    await runAxe(page, `advice-conflict-${theme}`, testInfo);
  });
}

test('a banner offers What to try and Show device', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.goto('/');
  await scanToFinish(page);
  const banner = page.locator('#alerts div.banner:not(.more)').filter({ hasText: 'Warning: Toner low' });
  await expect(banner).toHaveCount(1);
  const tryIt = banner.locator('details.btry');
  const meaning = tryIt.locator('.advice');
  await expect(meaning).toBeHidden();
  await tryIt.locator('summary').click();
  await expect(meaning).toContainText('What this means: Black Toner is at 15%.');
  await expect(meaning.locator('ol.steps li')).toHaveText(['Order a replacement soon.']);
  await page.keyboard.press('Escape');
  await expect(meaning).toBeHidden();
  await expect(tryIt.locator('summary')).toBeFocused();
  await banner.getByRole('button', { name: /^Show device/ }).click();
  await expect(page.locator('#drawer')).toBeVisible();
  await expect(page.locator('#drawer h2')).toHaveText('192.168.2.10');
  expect(rec.unmatched).toEqual([]);
});

test('a printer without status shows what it means and Open its page', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const list = fx.hosts();
  list[35] = fx.noStatusPrinter();
  await installMocks(page, { frames: framesWith(list) });
  await page.goto('/');
  await scanToFinish(page);
  await page.locator(`#hosts button.rowbtn[data-ip="${fx.NOSTATUS_IP}"]`).click();
  const block = page.locator('#printer-nostatus');
  await expect(block).toContainText("What this means: This printer doesn't report its status, so netdiag can only see whether it answers.");
  const link = block.getByRole('link', { name: /^Open its page/ });
  await expect(link).toHaveAttribute('href', `http://${fx.NOSTATUS_IP}/`);
  await expect(link).toHaveAttribute('target', '_blank');
  await expect(link).toHaveAttribute('rel', 'noopener noreferrer');
  // A printer that reports its status has no such block.
  await page.keyboard.press('Escape');
  await page.locator('#hosts button.rowbtn[data-ip="192.168.2.10"]').click();
  await expect(page.locator('#printer-nostatus')).toHaveCount(0);
});

for (const [platform, shown] of [['Win32', true], ['MacIntel', false]]) {
  test(`Open Location settings ${shown ? 'opens the page on Windows' : 'is hidden off Windows'}`, async ({ page }) => {
    await onPlatform(page, platform);
    await initStorage(page, { theme: 'dark', live: false });
    const rec = await installMocks(page, { health: 'location' });
    await page.goto('/');
    await page.locator('#tab-health').click();
    const wifi = card(page, 'Wi-Fi');
    await expect(wifi).toContainText('Windows hides Wi-Fi details until Location is turned on.');
    const btn = wifi.getByRole('button', { name: /^Open Location settings/ });
    if (!shown) {
      await expect(btn).toHaveCount(0);
      return;
    }
    await btn.click();
    await expect.poll(() => posts(rec, '/api/os/open').length).toBe(1);
    expect(JSON.parse(posts(rec, '/api/os/open')[0].body)).toEqual({ page: 'location' });
    await expect(page.locator('#health [role="status"].sub')).toContainText('Windows Settings is opening.');
  });
}

test('Open Location settings explains a 501', async ({ page }) => {
  await onPlatform(page, 'Win32');
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { health: 'location', osOpen: 501 });
  await page.goto('/');
  await page.locator('#tab-health').click();
  await card(page, 'Wi-Fi').getByRole('button', { name: /^Open Location settings/ }).click();
  await expect(page.locator('#health [role="status"].sub')).toContainText("netdiag can't open that page on this computer.");
});

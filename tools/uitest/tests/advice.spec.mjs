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

// A network the switch is already on for; it stays on for it.
const OTHER_NETWORK = 'n-0123456789ab';
const settingsPuts = (rec) => rec.requests.filter((r) => r.method === 'PUT' && r.path === '/api/settings');

test('"This network has no internet" makes Everything looks fine reachable, for this network only', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { health: 'offline', settings: { ...fx.settings(), noInternetNetworks: [OTHER_NETWORK] } });
  await page.goto('/');
  await page.locator('#tab-health').click();
  const dns = card(page, 'DNS resolving');
  await expect(dns.locator('.hs')).toContainText('Warning');
  await expect(dns).toContainText('If this network has no internet on purpose, turn on "This network has no internet" in Settings.');
  await expect(page.locator('#health-fine')).toBeHidden();

  // The card's Open Settings goes straight to the switch.
  await dns.getByRole('button', { name: /^Open Settings/ }).click();
  const dialog = page.locator('#settings-dialog');
  await expect(dialog).toHaveJSProperty('open', true);
  const sw = dialog.getByRole('switch', { name: 'This network has no internet' });
  await expect(sw).toBeFocused();
  await expect(sw).toBeEnabled();
  await expect(sw).not.toBeChecked();
  await expect(dialog.locator('#nointernet-help')).toContainText('It counts only on this network');
  await sw.check();
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(dialog).toHaveJSProperty('open', false);
  expect(settingsPuts(rec)).toHaveLength(1);
  const doc = JSON.parse(settingsPuts(rec)[0].body);
  expect(doc.noInternetNetworks).toEqual([OTHER_NETWORK, fx.NETWORK]);
  expect(doc).not.toHaveProperty('noInternet');
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

  // Turning it off again forgets this network only.
  await dns.getByRole('button', { name: /^Open Settings/ }).click();
  await expect(sw).toBeFocused();
  await expect(sw).toBeChecked();
  await sw.uncheck();
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(dialog).toHaveJSProperty('open', false);
  expect(JSON.parse(settingsPuts(rec).at(-1).body).noInternetNetworks).toEqual([OTHER_NETWORK]);
  await expect.poll(() => posts(rec, '/api/health/run').length).toBe(2);
  await expect(dns.locator('.hs')).toContainText('Warning');
  expect(rec.unmatched).toEqual([]);
});

test('a no-internet network with the switch on renders Everything looks fine', async ({ page }) => {
  await initStorage(page, { theme: 'light', live: false });
  await installMocks(page, { health: 'offline', settings: { ...fx.settings(), noInternetNetworks: [fx.NETWORK] } });
  await page.goto('/');
  await page.locator('#tab-health').click();
  await expect(page.locator('#health-fine')).toContainText(/OK Everything looks fine \(checked \d\d:\d\d\)/);
  await expect(page.locator('.hcard .hs', { hasText: /Warning|Failed/ })).toHaveCount(0);
});

// A finished scan of list with these conflicts, on the no-internet network
// with the switch on (its health checks are all fine).
function quietScan(list, conflicts = []) {
  const fine = fx.health({ state: 'offline', noInternet: true });
  return framesWith(list, conflicts).map((f) => (f.type === 'done' ? { ...f, data: { ...f.data, health: fine } } : f));
}
const switchOn = (triggers = {}) => {
  const s = fx.settings();
  return { ...s, noInternetNetworks: [fx.NETWORK], alerts: { ...s.alerts, triggers: { ...s.alerts.triggers, ...triggers } } };
};
const fineLine = /OK Everything looks fine \(checked \d\d:\d\d\)/;
const healthOnlyLine = /OK All health checks look fine \(checked \d\d:\d\d\)/;

test('Everything looks fine waits for printer alerts, low supplies and conflicts', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { settings: switchOn(), health: 'offline', frames: quietScan(fx.hosts(), [fx.conflict()]) });
  await page.goto('/');
  await scanToFinish(page);
  await page.locator('#tab-health').click();
  // The health checks are fine, but a printer has an alert and a low
  // supply, and two addresses share a MAC: the line speaks for the checks.
  const line = page.locator('#health-fine');
  await expect(line).toContainText(healthOnlyLine);
  await expect(line).not.toContainText('Everything');
  expect(rec.unmatched).toEqual([]);
});

// Printers with nothing wrong and no conflict; one device (not a printer)
// has gone offline.
function quietHosts() {
  return fx.hosts().map((h) => {
    if (h.isPrinter) h.printer = { ...h.printer, alerts: [], supplies: [] };
    if (h.ip === '192.168.2.100') Object.assign(h, { online: false, offlineSince: new Date().toISOString(), missedChecks: 2 });
    return h;
  });
}

for (const deviceOffline of [false, true]) {
  test(`an offline device ${deviceOffline ? 'counts' : 'does not count'} with "Any device goes offline" ${deviceOffline ? 'on' : 'off'}`, async ({ page }) => {
    await initStorage(page, { theme: 'light', live: false });
    const rec = await installMocks(page, { settings: switchOn({ deviceOffline }), health: 'offline', frames: quietScan(quietHosts()) });
    await page.goto('/');
    await scanToFinish(page);
    await page.locator('#tab-health').click();
    await expect(page.locator('#health-fine')).toContainText(deviceOffline ? healthOnlyLine : fineLine);
    expect(rec.unmatched).toEqual([]);
  });
}

// Serve GET /api/health with edit(report) applied to the 'offline' report.
async function healthReport(page, noInternet, edit) {
  await page.route('**/api/health', async (route) => {
    const h = fx.health({ state: 'offline', noInternet });
    edit(h);
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(h) });
  });
}

test('the switch waits until netdiag knows which network this is', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { health: 'offline' });
  // A report that names no network: an older one, or a router whose MAC
  // netdiag could not read.
  await healthReport(page, false, (h) => { delete h.network; });
  await page.goto('/');
  await page.locator('#nav-settings').click();
  const dialog = page.locator('#settings-dialog');
  await expect(dialog.getByRole('switch', { name: 'This network has no internet' })).toBeDisabled();
  await expect(dialog.locator('#nointernet-help')).toContainText("netdiag doesn't know which network this is yet.");
});

test('the health line waits for a check that could not run', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { health: 'offline', settings: { ...fx.settings(), noInternetNetworks: [fx.NETWORK] } });
  await healthReport(page, true, (h) => {
    h.checks[0] = { id: 'gateway', label: 'Gateway reachable', status: 'unavailable', detail: 'no default gateway detected',
      latencyMs: 0, data: { reason: 'no_gateway' } };
  });
  await page.goto('/');
  await page.locator('#tab-health').click();
  await expect(page.locator('.hcard')).toHaveCount(6);
  await expect(page.locator('#health-fine')).toBeHidden();
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
    // The maker comes from the conflict, so it shows whichever MAC answers now.
    await expect(cards.nth(0)).toContainText('Device A: Espressif Inc.');
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

// The plan's "Device B: Espressif · 24:6F:28:AA:BB:CC · seen at ...": a
// device that uses no other address, and that no listed device answers
// with now, still gets its maker, from the conflict's vendors.
test('a device that uses no other address still shows its maker', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { frames: framesWith(fx.flapHosts(3, false), [fx.flapConflict(3)]) });
  await page.goto('/');
  await scanToFinish(page);
  await page.locator(`#hosts button.rowbtn[data-ip="${fx.FLAP_IP}"]`).click();
  const cards = page.locator('#drawer .cpair .ccard');
  await expect(cards).toHaveCount(2);
  // 3 alternations end on the second MAC, so no device answers with the first.
  await expect(cards.nth(0)).toHaveText(/^Device A: Espressif Inc\. · 24:6F:28:AA:BB:CC · seen at \d\d:\d\d, \d\d:\d\d$/);
  await expect(cards.nth(1)).toHaveText(/^Device B: Hewlett Packard · 3C:52:82:11:22:33 · seen at \d\d:\d\d, \d\d:\d\d$/);
  await expect(page.locator('#drawer .cbox .chead strong')).toHaveText('Conflict');
  expect(rec.unmatched).toEqual([]);
});

// Only a mac_flap can be a "Conflict": a MAC that also took turns at the
// address, such as an extender's, stays a "Possible conflict" in its
// mac_multi_ip box, as its advice says it is normal.
test('mac_multi_ip with an alternating history still reads "Possible conflict"', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const list = fx.hosts();
  const at = list.find((h) => h.ip === '192.168.2.30');
  const ext = '00:1B:A9:AA:00:30';
  at.macHistory = [{ mac: ext, at: '2026-10-10T10:02:00Z' }, { mac: '24:6F:28:00:00:01', at: '2026-10-10T10:07:00Z' },
    { mac: ext, at: '2026-10-10T10:12:00Z' }];
  at.conflicts = [fx.CONFLICT_ID];
  const rec = await installMocks(page, { frames: framesWith(list, [fx.conflict()]) });
  await page.goto('/');
  await scanToFinish(page);
  await page.locator('#hosts button.rowbtn[data-ip="192.168.2.30"]').click();
  const box = page.locator('#drawer .cbox');
  await expect(box).toHaveCount(1);
  await expect(box.locator('.chead strong')).toHaveText('Possible conflict');
  await expect(box.locator('.chead use')).toHaveAttribute('href', 'icons.svg#i-triangle-alert');
  await expect(box.locator('.advice')).toContainText('This is normal for Wi-Fi extenders');
  expect(rec.unmatched).toEqual([]);
});

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

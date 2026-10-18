// What this means / What to try (v0.6 M7): health card advice with the raw
// detail under Technical details, the banner disclosure and Show device, the
// honest conflict view, the printer-without-status block, the Location
// settings button and the "This network has no internet" switch (D15).
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME } from '../support/mock.mjs';
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
    await page.goto(HOME);
    await page.locator('#tab-health').click();
    const cards = page.locator('.hcard');
    await expect(cards).toHaveCount(6);
    for (let i = 0; i < 6; i++) await expect(cards.nth(i).locator('.advice .meaning')).toContainText('What this means:');
    const meanings = {
      'Router': "This computer can't reach your router.",
      'Getting an address (DHCP)': "This computer didn't get an address from the router",
      'Name lookup (DNS)': "Your local network works, but internet names can't be looked up.",
      'Web server': "The web server isn't answering as expected (HTTP 503).",
      'Internet': "netdiag couldn't reach the internet.",
      'Wi-Fi': 'The Wi-Fi signal is weak (32%).',
    };
    for (const [label, m] of Object.entries(meanings)) await expect(card(page, label)).toContainText(m);
    await expect(page.locator('#health-fine')).toBeHidden();
    await runAxe(page, `advice-health-${theme}`, testInfo);

    // The raw detail waits under Technical details; Esc closes it again.
    const gw = card(page, 'Router');
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
  await page.goto(HOME);
  await page.locator('#tab-health').click();
  const dns = card(page, 'Name lookup (DNS)');
  await expect(dns.locator('.hs')).toContainText('Note');
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
  await expect(dialog.locator('#nointernet-help')).toHaveText(
    "Turn this on if this network has no internet on purpose. netdiag then stops warning that the internet can't be reached, on this network only.",
    { useInnerText: true });
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
  await expect(card(page, 'Internet')).toContainText('This network is set up without internet, so this check is only for information.');

  // Turning it off again forgets this network only.
  await dns.getByRole('button', { name: /^Open Settings/ }).click();
  await expect(sw).toBeFocused();
  await expect(sw).toBeChecked();
  await sw.uncheck();
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(dialog).toHaveJSProperty('open', false);
  expect(JSON.parse(settingsPuts(rec).at(-1).body).noInternetNetworks).toEqual([OTHER_NETWORK]);
  await expect.poll(() => posts(rec, '/api/health/run').length).toBe(2);
  await expect(dns.locator('.hs')).toContainText('Note');
  expect(rec.unmatched).toEqual([]);
});

test('a no-internet network with the switch on renders Everything looks fine', async ({ page }) => {
  await initStorage(page, { theme: 'light', live: false });
  await installMocks(page, { health: 'offline', settings: { ...fx.settings(), noInternetNetworks: [fx.NETWORK] } });
  await page.goto(HOME);
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

// Since v0.6 M8 the page keeps no copy of the card rules: the Health
// panel's line says "Everything looks fine" only when GET /api/problems
// does (which rules count is problems.Current's, tested in Go:
// TestCurrentRules, TestEverythingFine), so it never disagrees with the
// Problems right now card.
test('Everything looks fine waits for printer alerts, low supplies and conflicts', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { settings: switchOn(), health: 'offline', frames: quietScan(fx.hosts(), [fx.conflict()]) });
  await page.goto(HOME);
  await scanToFinish(page);
  // The health checks are fine, but a printer has an alert and a low
  // supply, and two addresses share a MAC: the line speaks for the checks.
  await expect(page.locator('#problems-now .pitem')).toHaveCount(4);
  await page.locator('#tab-health').click();
  const line = page.locator('#health-fine');
  await expect(line).toContainText(healthOnlyLine);
  await expect(line).not.toContainText('Everything');
  expect(rec.unmatched).toEqual([]);
});

test('the Health panel line follows the Problems right now card', async ({ page }) => {
  await initStorage(page, { theme: 'light', live: false });
  const rec = await installMocks(page, { settings: switchOn(), health: 'offline', problems: 'fine', frames: quietScan(fx.hosts(), [fx.conflict()]) });
  await page.goto(HOME);
  await scanToFinish(page);
  await expect(page.locator('#pn-fine')).toContainText(fineLine);
  await page.locator('#tab-health').click();
  await expect(page.locator('#health-fine')).toContainText(fineLine);
  // The card's answer changes (Check again reads it once more): so does the line.
  rec.problems = 'some';
  await page.locator('#health').getByRole('button', { name: 'Check again', exact: true }).click();
  await expect(page.locator('#health-fine')).toContainText(healthOnlyLine);
  expect(rec.unmatched).toEqual([]);
});

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
  await page.goto(HOME);
  await page.locator('#tab-health').click();
  // A button that opens Settings at the switch (here from a report that
  // still offers it) lands on the switch's help, which says why it is
  // grey, not on the first field of the dialog.
  const open = card(page, 'DNS resolving').getByRole('button', { name: /^Open Settings/ });
  await open.click();
  const dialog = page.locator('#settings-dialog');
  await expect(dialog).toHaveJSProperty('open', true);
  await expect(dialog.getByRole('switch', { name: 'This network has no internet' })).toBeDisabled();
  const help = dialog.locator('#nointernet-help');
  await expect(help).toHaveText("netdiag can't tell which network this is yet, so this can't be turned on. " +
    "Press Check again on Health; if this stays grey, netdiag can't read your router's details.", { useInnerText: true });
  await expect(help).toBeFocused();
  await expect(help).toBeInViewport();
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveJSProperty('open', false);
  await expect(open).toBeFocused();
});

test('the health line waits for a check that could not run', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { health: 'offline', settings: { ...fx.settings(), noInternetNetworks: [fx.NETWORK] } });
  await healthReport(page, true, (h) => {
    h.checks[0] = { id: 'gateway', label: 'Router', status: 'unavailable', detail: 'no default gateway detected',
      latencyMs: 0, data: { reason: 'no_gateway' } };
  });
  await page.goto(HOME);
  await page.locator('#tab-health').click();
  await expect(page.locator('.hcard')).toHaveCount(6);
  await expect(page.locator('#health-fine')).toBeHidden();
});

for (const [alt, heading] of [[0, 'Possible conflict'], [1, 'Possible conflict'], [2, 'Conflict']]) {
  test(`mac_flap with ${alt} alternation(s) reads "${heading}"`, async ({ page }) => {
    await initStorage(page, { theme: 'dark', live: false });
    const rec = await installMocks(page, { frames: framesWith(fx.flapHosts(alt), [fx.flapConflict(alt)]) });
    await page.goto(HOME);
    await scanToFinish(page);
    const btn = page.locator(`#hosts button.rowbtn[data-ip="${fx.FLAP_IP}"]`);
    // Two devices at one address: the row says CONFLICT, never NOTE.
    const row = page.locator('#hosts tr.host').filter({ has: page.locator(`button.rowbtn[data-ip="${fx.FLAP_IP}"]`) });
    await expect(row.locator('.badge.conflict')).toHaveText(/^CONFLICT/);
    await expect(row.locator('.badge.cnote')).toBeHidden();
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
    // The heading is in plain words, and says "Possible conflict" once.
    await expect(box.locator('.chead')).toHaveText(alt >= 2 ? 'Conflict: Two devices are using the same address'
      : 'Possible conflict: Two devices may be using the same address');
    await expect(box.locator('.advice .meaning')).toHaveText(alt >= 2
      ? 'What this means: Two devices are taking turns using 192.168.2.44, so both work badly.'
      : 'What this means: The device answering at 192.168.2.44 changed.');
    await expect(box.locator('.advice .meaning use')).toHaveAttribute('href', 'icons.svg#i-lightbulb');
    // The drawer shows both devices already: its steps say so, and promise nothing later.
    await expect(box.locator('.advice ol.steps li')).toHaveText(alt >= 2
      ? ['Compare the two devices (Device A and Device B).', 'Restart one of them so it gets a new address.',
        'If it keeps happening, ask whoever looks after the router to reserve an address for one of them.']
      : ['If you just replaced a device, this is expected.', 'If it happens again, netdiag will tell you.']);
    // The addresses and MACs wait under Technical details, in its small
    // monospace, below the size of the advice.
    const tech = box.locator('details.tech');
    const ips = tech.locator('p.hdetail', { hasText: 'IPs:' });
    await expect(ips).toBeHidden();
    await tech.locator('summary').click();
    await expect(ips).toHaveText(`IPs: ${fx.FLAP_IP} - MACs: ${fx.FLAP_MACS.join(', ')}`);
    const px = (loc) => loc.evaluate((e) => parseFloat(getComputedStyle(e).fontSize));
    expect(await px(tech.locator('p.hdetail').first())).toBeLessThan(await px(box.locator('.advice .meaning')));
    await page.keyboard.press('Escape');
    await expect(ips).toBeHidden();
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
  await page.goto(HOME);
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
// address, such as an extender's, stays a neutral "Note" in its
// mac_multi_ip box, as its advice says it is normal.
test('mac_multi_ip with an alternating history reads "Note"', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const list = fx.hosts();
  const at = list.find((h) => h.ip === '192.168.2.30');
  const ext = '00:1B:A9:AA:00:30';
  at.macHistory = [{ mac: ext, at: '2026-10-10T10:02:00Z' }, { mac: '24:6F:28:00:00:01', at: '2026-10-10T10:07:00Z' },
    { mac: ext, at: '2026-10-10T10:12:00Z' }];
  at.conflicts = [fx.CONFLICT_ID];
  const rec = await installMocks(page, { frames: framesWith(list, [fx.conflict()]) });
  await page.goto(HOME);
  await scanToFinish(page);
  await page.locator('#hosts button.rowbtn[data-ip="192.168.2.30"]').click();
  const box = page.locator('#drawer .cbox');
  await expect(box).toHaveCount(1);
  await expect(box.locator('.chead')).toHaveText('Note: One device uses several addresses');
  await expect(box.locator('.chead use')).toHaveAttribute('href', 'icons.svg#i-info');
  await expect(box.locator('.chead svg')).toHaveClass(/s-info/);
  await expect(box).toHaveClass(/note/);
  await expect(box.locator('.advice')).toContainText('This is normal for Wi-Fi extenders');
  // The row and the drawer's Status say it the same way: NOTE, the info shape.
  const status = page.locator('#drawer dl > div').filter({ has: page.locator('dt', { hasText: /^Status$/ }) }).locator('dd');
  await expect(status.locator('.badge.cnote')).toHaveText('NOTE');
  await expect(status.locator('.badge.cnote use')).toHaveAttribute('href', 'icons.svg#i-info');
  await expect(status.locator('.badge.conflict')).toBeHidden();
  const row = page.locator('#hosts tr.host').filter({ has: page.locator('button.rowbtn[data-ip="192.168.2.30"]') });
  await expect(row.locator('.badge.cnote')).toHaveText(/^NOTE/);
  await expect(row.locator('.badge.cnote use')).toHaveAttribute('href', 'icons.svg#i-info');
  await expect(row.locator('.badge.conflict')).toBeHidden();
  expect(rec.unmatched).toEqual([]);
});

test('the conflict pair stacks at 360 px and sits side by side when wide', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { frames: framesWith(fx.flapHosts(2), [fx.flapConflict(2)]) });
  await page.goto(HOME);
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
    await page.goto(HOME);
    await scanToFinish(page);
    await page.locator(`#hosts button.rowbtn[data-ip="${fx.FLAP_IP}"]`).click();
    await page.locator('#drawer .cbox details.tech summary').click();
    await runAxe(page, `advice-conflict-${theme}`, testInfo);
  });
}

test('a banner offers What to try and Show device', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.goto(HOME);
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
  const show = banner.getByRole('button', { name: /^Show device/ });
  await show.click();
  await expect(page.locator('#drawer')).toBeVisible();
  await expect(page.locator('#drawer h2')).toHaveText('192.168.2.10');
  // Keyboard and screen reader users are taken into the drawer, and Esc
  // brings them back to the button they started from.
  await expect(page.getByRole('button', { name: 'Close details' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(page.locator('#drawer')).toBeHidden();
  await expect(show).toBeFocused();
  expect(rec.unmatched).toEqual([]);
});

test('Show device from another view opens the drawer with focus in it', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.goto(HOME);
  await scanToFinish(page);
  await page.locator('#tab-health').click();
  const show = page.locator('#alerts div.banner').filter({ hasText: 'Warning: Toner low' }).getByRole('button', { name: /^Show device/ });
  await show.click();
  await expect(page.locator('#hosts')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Close details' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(show).toBeFocused();
  expect(rec.unmatched).toEqual([]);
});

// The conflict's banner says what is wrong in plain words; the MACs stay
// with the conflict, under Technical details in the drawer.
test('a conflict banner leads with plain words and offers Show device', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { frames: framesWith(fx.flapHosts(2), [fx.flapConflict(2)], [fx.flapNotice(2)]) });
  await page.goto(HOME);
  await scanToFinish(page);
  const banner = page.locator('#alerts div.banner:not(.more)').filter({ hasText: 'Network conflict' });
  await expect(banner.locator('.btext')).toContainText('Critical: Network conflict Two devices may be using 192.168.2.44.');
  await expect(banner).not.toContainText(fx.FLAP_MACS[0]);
  await banner.locator('details.btry summary').click();
  await expect(banner.locator('ol.steps li').first()).toHaveText('In netdiag, press Show device and compare the two devices (Device A and Device B).');
  await banner.getByRole('button', { name: /^Show device/ }).click();
  await expect(page.locator('#drawer .cbox .chead strong')).toHaveText('Conflict');
  expect(rec.unmatched).toEqual([]);
});

// A mac_flap no swap was seen for: the banner (and team chat) still title
// it a Network conflict, so its steps promise nothing the title already
// says.
test('a conflict banner without a seen swap promises only to tell you', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { frames: framesWith(fx.flapHosts(1), [fx.flapConflict(1)], [fx.flapNotice(1)]) });
  await page.goto(HOME);
  await scanToFinish(page);
  const banner = page.locator('#alerts div.banner:not(.more)').filter({ hasText: 'Network conflict' });
  await expect(banner.locator('.btext')).toContainText('Critical: Network conflict Two devices may be using 192.168.2.44.');
  await banner.locator('details.btry summary').click();
  await expect(banner.locator('ol.steps li')).toHaveText(['If you just replaced a device, this is expected.', 'If it happens again, netdiag will tell you.']);
  await expect(banner).not.toContainText('mark it as a conflict');
  await expect(banner.getByRole('button', { name: /^Show device/ })).toBeVisible();
  expect(rec.unmatched).toEqual([]);
});

// A health check that keeps failing: the banner has its own way to the
// Health view, where the check's card says what to try.
test('a health check banner offers Open Health', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { frames: framesWith(fx.hosts(), [], [fx.healthNotice()]) });
  await page.goto(HOME);
  await scanToFinish(page);
  const banner = page.locator('#alerts div.banner:not(.more)').filter({ hasText: 'Health check failed' });
  await banner.locator('details.btry summary').click();
  await expect(banner.locator('ol.steps li')).toHaveText(['In netdiag, press Open Health to see what to try.']);
  await expect(banner.getByRole('button', { name: /^Show device/ })).toBeHidden();
  await banner.getByRole('button', { name: /^Open Health/ }).click();
  await expect(page.locator('#health')).toBeVisible();
  await expect(page.locator('#health')).toBeFocused();
  expect(rec.unmatched).toEqual([]);
});

// On a phone, and on any short screen (a phone on its side, a small laptop
// window), the banners scroll away with the page: three of them, with What
// to try open, would otherwise cover most of the screen.
for (const [w, h] of [[844, 390], [1366, 650]]) {
  test(`banners scroll away on a short screen (${w}x${h})`, async ({ page }) => {
    await initStorage(page, { theme: 'dark', live: false });
    await installMocks(page, { frames: framesWith(fx.flapHosts(2), [fx.flapConflict(2)], [fx.notice(1), fx.flapNotice(2), fx.healthNotice()]) });
    await page.setViewportSize({ width: w, height: h });
    await page.goto(HOME);
    await scanToFinish(page);
    await expect(page.locator('#alerts div.banner:not(.more)')).toHaveCount(3);
    for (const s of await page.locator('#alerts details.btry summary').all()) await s.click();
    expect(await page.locator('#alerts').evaluate((e) => getComputedStyle(e).position)).toBe('static');
    await page.evaluate(() => window.scrollTo(0, 2000));
    const box = await page.locator('#alerts').boundingBox();
    expect(box.y + box.height, 'the banners scrolled off the top').toBeLessThanOrEqual(0);
  });
}

test('banners stay on top when wide and scroll away on a phone', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { frames: framesWith(fx.flapHosts(2), [fx.flapConflict(2)], [fx.notice(1), fx.flapNotice(2), fx.healthNotice()]) });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(HOME);
  await scanToFinish(page);
  const shown = page.locator('#alerts div.banner:not(.more)');
  await expect(shown).toHaveCount(3);
  for (const s of await page.locator('#alerts details.btry summary').all()) await s.click();
  const pos = () => page.locator('#alerts').evaluate((e) => getComputedStyle(e).position);
  expect(await pos()).toBe('static');
  await page.evaluate(() => window.scrollTo(0, 2000));
  const box = await page.locator('#alerts').boundingBox();
  expect(box.y + box.height, 'the banners scrolled off the top').toBeLessThanOrEqual(0);
  await page.setViewportSize({ width: 1440, height: 900 });
  expect(await pos()).toBe('sticky');
});

test('a printer without status shows what it means and Open its page', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const list = fx.hosts();
  list[35] = fx.noStatusPrinter();
  await installMocks(page, { frames: framesWith(list) });
  await page.goto(HOME);
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
    await page.goto(HOME);
    await page.locator('#tab-health').click();
    const wifi = card(page, 'Wi-Fi');
    await expect(wifi).toContainText('Windows hides Wi-Fi details until Location is turned on.');
    const btn = wifi.getByRole('button', { name: /^Open Location settings/ });
    if (!shown) {
      await expect(btn).toHaveCount(0);
      return;
    }
    // Open Location settings, then Check again, as the step says.
    await expect(wifi.locator('.advice ol.steps li')).toHaveText(['Turn on Location, then press Check again.']);
    await expect(wifi.locator('.aacts button:visible')).toHaveText([/^Open Location settings/, /^Check again/]);
    await btn.click();
    await expect.poll(() => posts(rec, '/api/os/open').length).toBe(1);
    expect(JSON.parse(posts(rec, '/api/os/open')[0].body)).toEqual({ page: 'location' });
    // The card says it, next to its Check again.
    await expect(wifi.locator('.hmsg')).toContainText('Windows Settings is opening.');
    const before = posts(rec, '/api/health/run').length;
    await wifi.getByRole('button', { name: /^Check again/ }).click();
    await expect.poll(() => posts(rec, '/api/health/run').length).toBe(before + 1);
  });
}

test('Open Location settings explains a 501', async ({ page }) => {
  await onPlatform(page, 'Win32');
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { health: 'location', osOpen: 501 });
  await page.goto(HOME);
  await page.locator('#tab-health').click();
  await card(page, 'Wi-Fi').getByRole('button', { name: /^Open Location settings/ }).click();
  await expect(card(page, 'Wi-Fi').locator('.hmsg')).toContainText("netdiag can't open that page on this computer.");
});

// A card's Check again says what it does next to the button, so on a
// phone the press never looks like it did nothing.
test('a card\'s Check again shows its progress on the card', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { health: 'all' });
  let release;
  const gate = new Promise((r) => { release = r; });
  await page.route('**/api/health/run', async (route) => {
    await gate;
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(fx.health({ state: 'all' })) });
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(HOME);
  await page.getByRole('button', { name: 'Menu' }).click();
  await page.locator('#tab-health').click();
  const gw = card(page, 'Gateway reachable');
  const btn = gw.getByRole('button', { name: /^(Check again|Checking)/ });
  await btn.evaluate((e) => e.scrollIntoView({ block: 'center' }));
  await btn.click();
  await expect(btn).toHaveText(/^Checking\.\.\./);
  await expect(btn).toBeDisabled();
  const msg = gw.locator('.hmsg');
  await expect(msg).toHaveAttribute('role', 'status');
  await expect(msg).toHaveText('Running checks...');
  await expect(msg).toBeInViewport();
  // The panel's own line stays quiet: one place says it.
  await expect(page.locator('#health-run ~ [role="status"]')).toHaveText('');
  release();
  await expect(msg).toHaveText('Checks updated.');
  await expect(btn).toHaveText(/^Check again/);
  await expect(btn).toBeEnabled();
  await expect(btn).toBeFocused();
  // The panel's Check again speaks under the cards again.
  await page.locator('#health-run').click();
  await expect(msg).toHaveText('');
  await expect(page.locator('#health-run ~ [role="status"]')).toHaveText('Checks updated.');
  expect(rec.unmatched).toEqual([]);
});

// One label for the same action, and one order: Check again first, then
// Settings or a link.
test('Check again has one label and comes first', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { health: 'all' });
  await page.goto(HOME);
  await page.locator('#tab-health').click();
  await expect(page.locator('#health').getByRole('button', { name: 'Recheck' })).toHaveCount(0);
  await expect(page.locator('#health-run')).toHaveText('Check again');
  for (const label of ['DNS resolving', 'Web server']) {
    await expect(card(page, label).locator('.aacts button:visible')).toHaveText([/^Check again/, /^Open Settings/]);
  }
});

// A redirect, or another address with its own certificate: the card's step
// names the address and the Use this address button, which has its own
// icon and puts the address in "Web server URL", where nothing is saved
// until Save. A sign-in page's one-time ticket stays out of the server's
// step (banner, chat), but the card still names the address.
for (const [web, addr, first] of [
  ['redirect', 'https://portal.example.com/login', 'It sends you to https://portal.example.com/login.'],
  ['redirect-query', 'https://portal.example.com/login?ticket=ST-1234', 'It sends you to https://portal.example.com/login?ticket=ST-1234.'],
  ['tls-other', 'https://intranet.local:8443/', 'The server sends you to https://intranet.local:8443/, which uses a different certificate.']]) {
  test(`the ${web} advice names the address and Use this address fills Settings`, async ({ page }) => {
    await initStorage(page, { theme: 'dark', live: false });
    const rec = await installMocks(page, { web });
    await page.goto(HOME);
    await page.locator('#tab-health').click();
    const wc = card(page, 'Web server');
    await expect(wc.locator('.advice ol.steps li').first()).toHaveText(first + ' If that is the right page, press Use this address below.');
    const use = wc.getByRole('button', { name: 'Use this address' });
    // Its own icon, not the settings gear of Open Settings next to it.
    await expect(use.locator('use')).toHaveAttribute('href', 'icons.svg#i-globe');
    await expect(wc.getByRole('button', { name: /^Open Settings/ }).locator('use')).toHaveAttribute('href', 'icons.svg#i-settings');
    await use.click();
    const dialog = page.locator('#settings-dialog');
    await expect(dialog).toHaveJSProperty('open', true);
    await expect(dialog.locator('#set-weburl')).toBeFocused();
    await expect(dialog.locator('#set-weburl')).toHaveValue(addr);
    expect(settingsPuts(rec)).toHaveLength(0);
    await page.keyboard.press('Escape');
    await expect(dialog).toHaveJSProperty('open', false);
    await expect(use).toBeFocused();
    expect(settingsPuts(rec)).toHaveLength(0);
    expect(rec.unmatched).toEqual([]);
  });
}

// A redirect loop, or another redirect than the one expected, is a status
// answer (the web check): the card offers no address to move to, though
// Technical details say where the server pointed.
for (const web of ['loop', 'redirect-expected']) {
  test(`a ${web} offers no address to use`, async ({ page }) => {
    await initStorage(page, { theme: 'dark', live: false });
    const rec = await installMocks(page, { web });
    await page.goto(HOME);
    await page.locator('#tab-health').click();
    const wc = card(page, 'Web server');
    await expect(wc.locator('.advice .meaning')).toContainText("The web server isn't answering as expected");
    await expect(wc.locator('.advice ol.steps li')).toHaveText(['Check that the server is switched on.', 'Ask whoever looks after it.']);
    await expect(wc.getByRole('button', { name: 'Use this address' })).toBeHidden();
    await expect(wc.locator('.aacts button:visible')).toHaveText([/^Check again/, /^Open Settings/]);
    await wc.locator('details.tech summary').click();
    await expect(wc.locator('details.tech .hdetail')).toContainText('https://intranet.local/');
    expect(rec.unmatched).toEqual([]);
  });
}

// The printer block starts with the plain words; how netdiag recognised
// the printer waits under Technical details.
test('the printer block puts its plain advice before the raw reason', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const list = fx.hosts();
  list[35] = fx.noStatusPrinter();
  await installMocks(page, { frames: framesWith(list) });
  await page.goto(HOME);
  await scanToFinish(page);
  await page.locator(`#hosts button.rowbtn[data-ip="${fx.NOSTATUS_IP}"]`).click();
  const section = page.locator('#drawer section').filter({ has: page.getByRole('heading', { name: 'Printer', exact: true }) });
  const first = section.locator('h3 ~ *:visible').first();
  await expect(first).toHaveId('printer-nostatus');
  const reason = section.locator('details.tech p.hdetail');
  await expect(reason).toBeHidden();
  await section.locator('details.tech summary').click();
  await expect(reason).toHaveText('Recognised by: port 9100 (raw printing) open');
  // A printer with status data names the reason as a short line instead.
  await page.keyboard.press('Escape');
  await page.keyboard.press('Escape');
  await page.locator('#hosts button.rowbtn[data-ip="192.168.2.10"]').click();
  await expect(page.locator('#drawer section').filter({ has: page.getByRole('heading', { name: 'Printer', exact: true }) }).locator('p.sub').first())
    .toHaveText('Recognised by: hrDeviceType=printer');
});

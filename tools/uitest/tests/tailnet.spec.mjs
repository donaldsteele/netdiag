// v0.6.1 Tailnet: the nav item (only while Tailscale is on this computer),
// the map and list with Check path, the states that explain what to do, the
// Tailscale card on Health (not a seventh pill), the three alert switches and
// Open Tailnet next to a Tailscale alert. Mock data uses invented names only.
import path from 'node:path';
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME, SESSION } from '../support/mock.mjs';
import { runAxe } from '../support/axe.mjs';
import { scanToFinish } from '../support/steps.mjs';
import { OUT_DIR, THEMES } from '../support/config.mjs';
import * as fx from '../fixtures/index.mjs';

const row = (page, id) => page.locator('#tnd-' + id);
const node = (page, name) => page.locator('#tailnet .tnspot button.tnnode', { hasText: name + ':' });

async function openTailnet(page, opts = {}, theme = 'dark') {
  await initStorage(page, { theme, live: false });
  const rec = await installMocks(page, opts);
  await page.goto(HOME);
  await page.locator('#tab-tailnet').click();
  await expect(page.locator('#tab-tailnet')).toHaveAttribute('aria-current', 'page');
  return rec;
}

for (const theme of THEMES) {
  test(`the Tailnet view: header, map and list in words, ${theme}`, async ({ page }, testInfo) => {
    const rec = await openTailnet(page, {}, theme);
    const view = page.locator('#tailnet');
    await expect(view.locator('#tn-state')).toContainText('Tailscale: Connected');
    await expect(view.locator('#tn-state')).toContainText('Tailnet ' + fx.TAILNET_SUFFIX);
    await expect(view.locator('#tn-state')).toContainText('Tailscale 1.90.0');
    await expect(view.locator('#tn-summary')).toHaveText(`${fx.TAILNET_ONLINE} of 7 other devices online.`);
    await expect(view.locator('#tn-self')).toContainText('office-pc');
    await expect(view.locator('#tn-self')).toContainText('100.101.102.103');
    await expect(view.locator('#tn-self').getByRole('button', { name: 'Copy Tailscale address 100.101.102.103' })).toBeVisible();
    const admin = view.locator('#tn-admin');
    await expect(admin).toHaveAttribute('href', fx.TAILNET_ADMIN);
    await expect(admin).toHaveAttribute('target', '_blank');
    await expect(admin).toContainText('Open Tailscale admin console');

    // The map: this computer in the middle and one node per device.
    await expect(view.locator('.tnspot button.tnnode')).toHaveCount(7);
    await expect(view.locator('.tnself')).toContainText('This computer');
    await expect(view.locator('.tnself')).toContainText('office-pc');
    // One line per online device, styled by path; offline devices have none.
    const lines = (cls) => view.locator('path.' + cls).first().getAttribute('d').then((d) => (d.match(/M/g) || []).length);
    expect(await lines('ln-direct')).toBe(3);
    expect(await lines('ln-relayed')).toBe(1);
    expect(await lines('ln-idle')).toBe(1);
    await expect(view.locator('.tnvia')).toHaveText(['via fra']);
    await expect(view.locator('.tp-offline')).toHaveCount(2);
    // Every status is in words too: the legend and each node's name.
    await expect(view.locator('.tnlegend')).toContainText('Direct:');
    await expect(view.locator('.tnlegend')).toContainText('Relayed:');
    await expect(view.locator('.tnlegend')).toContainText('Idle:');
    await expect(view.locator('.tnlegend')).toContainText('Offline:');
    await expect(node(page, 'cloud-vm')).toHaveAccessibleName(/^cloud-vm: Online, Relayed via fra, exit node\./);
    await expect(node(page, 'phone')).toHaveAccessibleName(/^phone: Offline\./);

    // The list: status word, path word, notes.
    await expect(view.locator('.tnrow')).toHaveCount(7);
    await expect(row(page, 'nPI1CNTRL').locator('.tnst')).toHaveText('Online');
    await expect(row(page, 'nPI1CNTRL').locator('.tnpath')).toHaveText('Direct (192.168.1.40:41641)');
    await expect(row(page, 'nPI1CNTRL')).toContainText('chat-pi.' + fx.TAILNET_SUFFIX);
    await expect(row(page, 'nPI1CNTRL').getByRole('button', { name: 'Copy Tailscale address 100.101.1.2' })).toBeVisible();
    await expect(row(page, 'nVM2CNTRL').locator('.tnpath')).toHaveText('Relayed via fra');
    await expect(row(page, 'nVM2CNTRL')).toContainText('Can be an exit node');
    await expect(row(page, 'nVM2CNTRL')).toContainText('tag:server');
    await expect(row(page, 'nLT3CNTRL').locator('.tnst')).toHaveText('Idle');
    await expect(row(page, 'nLT3CNTRL').locator('.tnpath')).toHaveText('No traffic yet');
    await expect(row(page, 'nLT3CNTRL')).toContainText('macOS');
    await expect(row(page, 'nNS4CNTRL')).toContainText('Gives access to 192.168.10.0/24 (subnet router)');
    await expect(row(page, 'nPH5CNTRL').locator('.tnkey')).toHaveText('Key expires in 3 days');
    await expect(row(page, 'nOD6CNTRL').locator('.tnst')).toHaveText('Offline');
    await expect(row(page, 'nOD6CNTRL').locator('.tnpath')).toBeHidden();
    await expect(row(page, 'nOD6CNTRL')).toContainText('Last seen');
    await expect(row(page, 'nOD6CNTRL').getByRole('button', { name: /^Check path/ })).toBeHidden();
    await expect(row(page, 'nPN7CNTRL')).toContainText('Android');
    // Status icons differ by status, not colour alone.
    const shapes = await view.locator('.tnrow .tnst use').evaluateAll((els) => [...new Set(els.map((e) => e.getAttribute('href')))]);
    expect(shapes.sort()).toEqual(['icons.svg#i-circle-check', 'icons.svg#i-circle-dot', 'icons.svg#i-wifi-off']);
    // The tailnet is named by its suffix; no account name or address.
    await expect(view).not.toContainText('@');

    await page.screenshot({ path: path.join(OUT_DIR, 'shots', `tailnet-${theme}.png`), fullPage: true });
    await runAxe(page, `tailnet-${theme}`, testInfo);
    expect(rec.unmatched).toEqual([]);
  });
}

test('a map node picks its row in the list, by mouse and by keyboard', async ({ page }) => {
  const rec = await openTailnet(page);
  await node(page, 'nas').click();
  await expect(node(page, 'nas')).toHaveAttribute('aria-pressed', 'true');
  await expect(row(page, 'nNS4CNTRL')).toHaveClass(/picked/);
  await expect(row(page, 'nNS4CNTRL')).toBeInViewport();
  expect((await page.locator('path.ln-pick').getAttribute('d')).startsWith('M50 50L')).toBe(true);
  await node(page, 'cloud-vm').focus();
  await page.keyboard.press('Enter');
  await expect(node(page, 'cloud-vm')).toHaveAttribute('aria-pressed', 'true');
  await expect(node(page, 'nas')).toHaveAttribute('aria-pressed', 'false');
  await expect(row(page, 'nVM2CNTRL')).toHaveClass(/picked/);
  await expect(row(page, 'nNS4CNTRL')).not.toHaveClass(/picked/);
  await expect(page.locator('path.ln-pick')).toHaveClass(/ln-relayed/);
  // Pressing the picked node again lets it go.
  await page.keyboard.press('Space');
  await expect(node(page, 'cloud-vm')).toHaveAttribute('aria-pressed', 'false');
  await expect(page.locator('.tnrow.picked')).toHaveCount(0);
  // A click on a row picks it too.
  await row(page, 'nLT3CNTRL').locator('h4').click();
  await expect(node(page, 'laptop')).toHaveAttribute('aria-pressed', 'true');
  expect(rec.unmatched).toEqual([]);
});

test('Check path says the path and time in words, and asks to wait when pressed too soon', async ({ page }) => {
  const rec = await openTailnet(page);
  const pi = row(page, 'nPI1CNTRL');
  await pi.getByRole('button', { name: 'Check path to chat-pi' }).click();
  await expect(pi.locator('.tnres')).toHaveText('Direct, 1 ms');
  await expect(pi.locator('.tnres use')).toHaveAttribute('href', 'icons.svg#i-circle-check');
  const sent = rec.requests.filter((r) => r.path === '/api/tailnet/ping');
  expect(sent).toHaveLength(1);
  expect(sent[0]).toMatchObject({ method: 'POST', session: SESSION });
  expect(sent[0].contentType).toContain('application/json');
  expect(JSON.parse(sent[0].body)).toEqual({ ip: '100.101.1.2' });
  // The same device again within 5 s: 429, said politely.
  await pi.getByRole('button', { name: 'Check path to chat-pi' }).click();
  await expect(pi.locator('.tnres')).toHaveText('Wait a few seconds before checking again.');
  await expect(pi.locator('.tnres use')).toHaveAttribute('href', 'icons.svg#i-triangle-alert');
  // A relayed device names its relay.
  await page.waitForTimeout(1100);
  const vm = row(page, 'nVM2CNTRL');
  await vm.getByRole('button', { name: 'Check path to cloud-vm' }).click();
  await expect(vm.locator('.tnres')).toHaveText('Relayed via fra, 42 ms');
  // No answer, in plain words.
  rec.pingAnswer = { status: 200, body: { ip: '100.101.1.4', name: 'laptop', ok: false, error: 'timed out after 10 seconds' } };
  const lt = row(page, 'nLT3CNTRL');
  await lt.getByRole('button', { name: 'Check path to laptop' }).click();
  await expect(lt.locator('.tnres')).toHaveText('No answer: Timed out after 10 seconds.');
  // The server's own error words otherwise.
  rec.pingAnswer = { status: 409, body: { error: "Tailscale isn't connected on this computer" } };
  const nas = row(page, 'nNS4CNTRL');
  await nas.getByRole('button', { name: 'Check path to nas' }).click();
  await expect(nas.locator('.tnres')).toHaveText("Tailscale isn't connected on this computer.");
  expect(rec.pings).toEqual(['100.101.1.2', '100.101.1.2', '100.101.1.3', '100.101.1.4', '100.101.1.5']);
  expect(rec.unmatched).toEqual([]);
});

test('without Tailscale the nav item and the Health card stay hidden', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { tailnet: 'not-installed' });
  await page.goto(HOME);
  await expect.poll(() => rec.tailnetCalls).toBeGreaterThan(0);
  await expect(page.locator('#tab-tailnet')).toBeHidden();
  await expect(page.locator('#tab-devices')).toBeVisible();
  await page.locator('#tab-health').click();
  await expect(page.locator('.hcard').first()).toBeVisible();
  await expect(page.locator('#tailscale-card')).toHaveCount(0);
  // If the view is opened anyway (a saved view), it says so plainly.
  await page.evaluate(() => window.Alpine.$data(document.querySelector('.shell')).showView('tailnet'));
  await expect(page.locator('#tn-explain')).toContainText("Tailscale isn't on this computer.");
  await expect(page.locator('#tailnet .tnmap')).toHaveCount(0);
  await expect(page.locator('#tn-admin')).toBeHidden();
  expect(rec.unmatched).toEqual([]);
});

for (const [state, word, says] of [
  ['stopped', 'Not connected', 'Open the Tailscale app and connect.'],
  ['needs-login', 'Needs sign-in', 'Tailscale needs you to sign in.'],
  ['error', 'Not connected', "netdiag couldn't read Tailscale on this computer."],
]) {
  test(`Tailscale ${state}: the view says what to do and shows no map`, async ({ page }, testInfo) => {
    const rec = await openTailnet(page, { tailnet: state });
    await expect(page.locator('#tn-state')).toContainText('Tailscale: ' + word);
    await expect(page.locator('#tn-state use').first()).toHaveAttribute('href', 'icons.svg#i-triangle-alert');
    await expect(page.locator('#tn-explain')).toContainText(says);
    await expect(page.locator('#tailnet .tnmap')).toHaveCount(0);
    await expect(page.locator('#tailnet .tnrow')).toHaveCount(0);
    await expect(page.locator('#tn-admin')).toBeVisible();
    await runAxe(page, `tailnet-${state}`, testInfo);
    // The Health card says it too, as a shape plus a word.
    await page.locator('#tab-health').click();
    const card = page.locator('#tailscale-card');
    await expect(card).toBeVisible();
    await expect(card.locator('.hs')).toHaveText(state === 'error' ? 'Could not run' : 'Note');
    await expect(card.locator('.advice .meaning')).toBeVisible();
    await expect(card.locator('#ts-open')).toBeHidden();
    expect(rec.unmatched).toEqual([]);
  });
}

test('Health: the Tailscale card sits below the six checks and is not a pill', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.goto(HOME);
  await scanToFinish(page);
  await expect(page.locator('.hstrip .hpill')).toHaveCount(6);
  await expect(page.locator('.hstrip')).not.toContainText('Tailscale');
  await page.locator('#tab-health').click();
  await expect(page.locator('.hcard')).toHaveCount(6);
  const card = page.locator('#tailscale-card');
  await expect(card).toBeVisible();
  await expect(card.getByRole('heading', { name: 'Tailscale' })).toBeVisible();
  await expect(card.locator('.hs')).toHaveText('OK');
  await expect(card.locator('.hs use')).toHaveAttribute('href', 'icons.svg#i-circle-check');
  await expect(card).toContainText(`Connected to ${fx.TAILNET_SUFFIX}. ${fx.TAILNET_ONLINE} of 7 other devices online.`);
  // Below the check cards, outside their list.
  expect(await page.evaluate(() => {
    const list = document.querySelector('#health .hcards'), c = document.getElementById('tailscale-card');
    return !list.contains(c) && !!(list.compareDocumentPosition(c) & Node.DOCUMENT_POSITION_FOLLOWING);
  })).toBe(true);
  await expect(page.locator('#health')).not.toContainText('Warning');
  // Check again reads the tailnet again; Open Tailnet goes to the view.
  const before = rec.tailnetCalls;
  await card.locator('#ts-recheck').click();
  await expect.poll(() => rec.tailnetCalls).toBeGreaterThan(before);
  await card.locator('#ts-open').click();
  await expect(page.locator('#tailnet')).toBeVisible();
  await expect(page.locator('#tailnet')).toBeFocused();
  await page.locator('#tab-health').click();
  await expect(page.locator('.hstrip .hpill')).toHaveCount(6);
  expect(rec.unmatched).toEqual([]);
});

test("Tailscale's own messages show as notes, and the card says Note", async ({ page }) => {
  const rec = await openTailnet(page, { tailnet: { health: ['Some peers are advertising routes but --accept-routes is false'] } });
  await expect(page.locator('#tn-health')).toContainText('Messages from Tailscale');
  await expect(page.locator('#tn-health li')).toHaveText(['Note Some peers are advertising routes but --accept-routes is false']);
  await page.locator('#tab-health').click();
  const card = page.locator('#tailscale-card');
  await expect(card.locator('.hs')).toHaveText('Note');
  await expect(card.locator('.hs use')).toHaveAttribute('href', 'icons.svg#i-info');
  await expect(page.locator('#health')).not.toContainText('Warning');
  expect(rec.unmatched).toEqual([]);
});

test('the view reads the tailnet every 15 s while it is shown, and stops when it is not', async ({ page }) => {
  await page.clock.install();
  const rec = await openTailnet(page);
  await expect(page.locator('#tailnet .tnrow')).toHaveCount(7);
  const shown = rec.tailnetCalls;
  await page.clock.runFor(15500);
  await expect.poll(() => rec.tailnetCalls).toBe(shown + 1);
  // A device going offline between reads goes to the activity log.
  rec.tailnet = { devices: fx.tailnetDevices().map((d) => d.name === 'nas' ? { ...d, online: false, path: 'offline' } : d) };
  await page.clock.runFor(15000);
  await expect.poll(() => rec.tailnetCalls).toBe(shown + 2);
  await expect(row(page, 'nNS4CNTRL').locator('.tnst')).toHaveText('Offline');
  await page.locator('#tab-activity').click();
  await expect(page.locator('#log')).toContainText('nas is offline on the tailnet');
  const hidden = rec.tailnetCalls;
  await page.clock.runFor(60000);
  expect(rec.tailnetCalls).toBe(hidden);
  expect(rec.unmatched).toEqual([]);
});

test('Settings > Alerts has a switch for each Tailscale alert', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, {});
  await page.goto(HOME);
  await expect.poll(() => page.evaluate(() => !!window.Alpine.$data(document.querySelector('.shell')).settings)).toBe(true);
  await page.evaluate(() => document.querySelector('#nav-settings').click());
  const dlg = page.locator('#settings-dialog');
  for (const name of ['Tailscale stops on this computer', 'A tailnet computer goes offline (not phones)', 'A Tailscale key is about to expire']) {
    await expect(dlg.getByRole('checkbox', { name })).toBeChecked();
  }
  await dlg.getByRole('checkbox', { name: 'A tailnet computer goes offline (not phones)' }).uncheck();
  await dlg.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(dlg).toBeHidden();
  const put = rec.requests.filter((r) => r.method === 'PUT' && r.path === '/api/settings').pop();
  expect(JSON.parse(put.body).alerts.triggers).toMatchObject({ tailscaleDown: true, tailnetDeviceOffline: false, tailnetKeyExpiring: true });
  expect(rec.unmatched).toEqual([]);
});

test('a Tailscale alert offers Open Tailnet next to Dismiss', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const down = { id: 'n-95', trigger: 'tailscaleDown', severity: 'warn', title: 'Tailscale stopped',
    message: 'Tailscale stopped on this computer.', key: 'tailscaleDown', at: new Date().toISOString() };
  const rec = await installMocks(page, { frames: fx.framesWith(fx.hosts(), [], [down]) });
  await page.goto(HOME);
  await scanToFinish(page);
  const banner = page.locator('#alerts div.banner', { hasText: 'Tailscale stopped' });
  await expect(banner).toContainText('Warning: Tailscale stopped');
  await banner.getByRole('button', { name: 'Open Tailnet Tailscale stopped' }).click();
  await expect(page.locator('#tailnet')).toBeVisible();
  await expect(page.locator('#tab-tailnet')).toHaveAttribute('aria-current', 'page');
  expect(rec.unmatched).toEqual([]);
});

test('reduced motion: picking a node runs no animation', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const rec = await openTailnet(page);
  await node(page, 'chat-pi').click();
  await expect(row(page, 'nPI1CNTRL')).toHaveClass(/picked/);
  expect(await page.evaluate(() => document.getAnimations().map((a) => a.animationName || a.transitionProperty))).toEqual([]);
  expect(rec.unmatched).toEqual([]);
});

test('at 360 px the map and list fit without a sideways scroll', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 800 });
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, {});
  await page.goto(HOME);
  await page.locator('.menubtn').click();
  await page.locator('#tab-tailnet').click();
  await expect(page.locator('#tailnet .tnrow')).toHaveCount(7);
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0);
  // Every node stays inside the map.
  const out = await page.evaluate(() => {
    const box = document.querySelector('.tnbox').getBoundingClientRect();
    return [...document.querySelectorAll('.tnspot .tnnode, .tnspot .tnlabel')].filter((el) => {
      const r = el.getBoundingClientRect();
      return r.left < box.left - 1 || r.right > box.right + 1 || r.top < box.top - 1 || r.bottom > box.bottom + 1;
    }).map((el) => el.textContent.trim());
  });
  expect(out).toEqual([]);
  await page.screenshot({ path: path.join(OUT_DIR, 'shots', 'tailnet-narrow-dark.png'), fullPage: true });
  expect(rec.unmatched).toEqual([]);
});

// Team chat (bubbles), v0.6 M10 (4.10): Settings > Team chat (bubbles), the
// connect wizard (find the server, compare the fingerprint, the admin
// approves, the channel, Send test alert, Done) and the header status tool.
// Every route is mocked like internal/server/handlers_chat.go.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME, SESSION } from '../support/mock.mjs';
import * as fx from '../fixtures/index.mjs';
import { runAxe } from '../support/axe.mjs';
import { THEMES, WEB_DIR } from '../support/config.mjs';
import fs from 'node:fs/promises';
import path from 'node:path';

const settingsDlg = (page) => page.locator('#settings-dialog');
const wizard = (page) => page.locator('#chat-dialog');
const puts = (rec) => rec.requests.filter((r) => r.method === 'PUT' && r.path === '/api/settings');

function watchErrors(page) {
  const errors = [];
  page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`));
  page.on('console', (m) => { if (m.type() === 'error' && !/favicon|Failed to load resource/i.test(m.text())) errors.push(`console: ${m.text()}`); });
  return errors;
}

function clean(rec, errors) {
  expect(errors, 'page errors').toEqual([]);
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
  expect(rec.external, 'external requests').toEqual([]);
  expect(rec.unauthorized, 'requests the session guard refused').toEqual([]);
  expect(rec.requests.filter((r) => r.session !== SESSION), 'requests without the session token').toEqual([]);
}

async function open(page, opts = {}, theme = 'dark') {
  await initStorage(page, { theme, live: false });
  const rec = await installMocks(page, { scenario: 'scan', ...opts });
  await page.goto(HOME);
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('netdiag');
  return rec;
}

async function openSettings(page) {
  await page.evaluate(() => document.querySelector('#nav-settings').click());
  await expect(settingsDlg(page)).toHaveJSProperty('open', true);
}

// Settings > Team chat (bubbles) > Connect to team chat, up to the found servers.
async function startWizard(page) {
  await openSettings(page);
  await settingsDlg(page).locator('#chat-status').scrollIntoViewIfNeeded();
  await expect(settingsDlg(page).locator('#chat-status')).toHaveText('Status: Not connected');
  await settingsDlg(page).getByRole('button', { name: 'Connect to team chat' }).click();
  await expect(settingsDlg(page)).toHaveJSProperty('open', false);
  await expect(wizard(page)).toHaveJSProperty('open', true);
}

test('the wizard: find, compare the fingerprint, approve, channel, test alert, done', async ({ page }) => {
  const errors = watchErrors(page);
  const rec = await open(page);
  await expect(page.locator('#chat-tool')).toBeHidden();
  await startWizard(page);
  const w = wizard(page);

  // Step 2: the servers netdiag found.
  await expect(w.locator('#wiz-list')).toBeVisible();
  await expect(w.locator('#wiz-list .chatcard')).toHaveText(/Office · 192\.168\.2\.20/);
  await w.getByRole('button', { name: 'Use this server: Office' }).click();

  // Step 3: the first four groups, large and bold, then the other twelve.
  await expect(w.getByRole('heading', { name: 'Is this the right server?' })).toBeVisible();
  await expect(w.locator('#wiz-fp-start')).toHaveText('3F2A 9C10 77B4 E0D1');
  await expect(w.locator('#wiz-fp-rest')).toHaveText(fx.CHAT_GROUPS.slice(4).join(' '));
  expect(Number(await w.locator('#wiz-fp-start').evaluate((e) => getComputedStyle(e).fontWeight))).toBeGreaterThanOrEqual(700);
  await expect(w).toContainText('This computer: ' + fx.SELF_IP);
  await expect(w).not.toContainText('fpPrefix');
  await w.getByRole('button', { name: 'It matches' }).click();

  // Step 4: the code, how long it works, and the admin page.
  await expect(w.locator('#wiz-code')).toHaveText('123 456');
  await expect(w.locator('#wiz-approve')).toContainText(/This code works for (9:\d\d|10:00) more\./);
  await expect(w.locator('#wiz-admin')).toHaveAttribute('href', 'https://192.168.2.20:28225/admin/pairing');
  await expect(w.locator('#wiz-admin')).toHaveAttribute('target', '_blank');
  const pair = rec.requests.find((r) => r.path === '/api/bubbles/pair');
  expect(JSON.parse(pair.body)).toEqual({ address: '192.168.2.20:28225', fingerprint: fx.CHAT_FP, confirmed: true });

  // Steps 5-7: approved, the one channel is used, a test alert.
  await expect(w.locator('#wiz-ready')).toBeVisible({ timeout: 10_000 });
  await expect(w.locator('#wiz-ready')).toContainText('Alerts will go to #netdiag-alerts.');
  await expect(w.getByRole('button', { name: 'Send test alert' })).toBeFocused();
  await expect.poll(() => puts(rec).length).toBe(1);
  const doc = JSON.parse(puts(rec)[0].body);
  expect(doc.chat).toEqual({ enabled: true, minSeverity: 'warning' });
  expect(doc.mcp).toEqual(fx.settings().mcp);
  await w.getByRole('button', { name: 'Send test alert' }).click();
  await expect(w.locator('#wiz-test-msg')).toHaveText('Test alert sent to #netdiag-alerts and closed again. Check that it appeared in bubbles.');
  await w.getByRole('button', { name: 'Finish' }).click();

  // Step 8 (D10): said plainly.
  await expect(w.locator('#wiz-done')).toContainText('netdiag sends alerts to #netdiag-alerts while it is watching this network. Leave netdiag open with Live mode ticked.');
  await w.getByRole('button', { name: 'Close' }).click();

  // Back in Settings, connected.
  await expect(w).toHaveJSProperty('open', false);
  const s = settingsDlg(page);
  await expect(s).toHaveJSProperty('open', true);
  await expect(s.locator('#chat-status')).toHaveText('Status: Connected');
  await expect(s.locator('#chat-where')).toHaveText('Sending to #netdiag-alerts on Office (192.168.2.20)');
  await expect(s.locator('#chat-enabled')).toBeChecked();
  await expect(s.locator('#chat-level')).toHaveValue('warning');
  await expect(s.locator('#chat-watching')).toHaveText('Alerts go to team chat only while netdiag is watching this network. Leave netdiag open with Live mode ticked.');
  await expect(s.locator('#chat-test')).toBeFocused();
  await expect(page.locator('#chat-tool')).toBeVisible();
  await expect(page.locator('#chat-tool')).toHaveAttribute('title', 'Team chat: connected');
  expect(rec.chatCalls).toEqual(['POST /api/bubbles/discover', 'POST /api/bubbles/inspect', 'POST /api/bubbles/pair', 'POST /api/bubbles/test']);
  clean(rec, errors);
});

test("It doesn't stops the wizard; Esc closes it and gives focus back to Connect", async ({ page }) => {
  const rec = await open(page);
  await startWizard(page);
  const w = wizard(page);
  await w.getByRole('button', { name: 'Use this server: Office' }).click();
  await w.getByRole('button', { name: "It doesn't" }).click();
  await expect(w.locator('#wiz-nomatch')).toContainText('Stop here and tell your chat admin. Something on the network may be pretending to be the chat server.');
  await expect(w.locator('#wiz-nomatch [role=alert]')).toBeVisible();
  await w.getByRole('button', { name: 'Close' }).click();
  await expect(settingsDlg(page).locator('#chat-connect')).toBeFocused();
  expect(rec.chatCalls).not.toContain('POST /api/bubbles/pair');

  // Esc on the code step stops waiting, and focus returns to the button.
  await settingsDlg(page).locator('#chat-connect').click();
  await wizard(page).getByRole('button', { name: 'Use this server: Office' }).click();
  await wizard(page).getByRole('button', { name: 'It matches' }).click();
  await expect(wizard(page).locator('#wiz-code')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(wizard(page)).toHaveJSProperty('open', false);
  await expect(settingsDlg(page)).toHaveJSProperty('open', true);
  await expect(settingsDlg(page).locator('#chat-connect')).toBeFocused();
  await expect.poll(() => rec.chatCalls.at(-1)).toBe('DELETE /api/bubbles/pair');
  // Esc in Settings still closes Settings.
  await page.keyboard.press('Escape');
  await expect(settingsDlg(page)).toHaveJSProperty('open', false);
  expect(rec.unmatched).toEqual([]);
});

test('nothing found: type the address, refused for a server on the internet', async ({ page }) => {
  const rec = await open(page, { found: { servers: 0 } });
  await startWizard(page);
  const w = wizard(page);
  await expect(w.locator('#wiz-none')).toHaveText('No chat server found automatically. Windows Firewall or a separate Wi-Fi network can hide it.');
  await w.getByRole('button', { name: 'Type the address' }).click();
  await expect(w.locator('#chat-address')).toBeFocused();
  await w.locator('#chat-address').fill('203.0.113.5');
  await w.getByRole('button', { name: 'Check this server' }).click();
  await expect(w.locator('#wiz-error')).toHaveText('Use the address of a chat server on the local network, for example 192.168.1.20 or office.local.');
  await w.locator('#chat-address').fill('192.168.2.20');
  await w.getByRole('button', { name: 'Check this server' }).click();
  await expect(w.locator('#wiz-fp-start')).toHaveText('3F2A 9C10 77B4 E0D1');
  await expect(w.locator('#wiz-error')).toBeHidden();
  expect(rec.unmatched).toEqual([]);
});

test('the tray server, a connection string, and a choice of channels', async ({ page }) => {
  const errors = watchErrors(page);
  const two = [fx.CHAT_CHANNEL, { id: 'C2', name: 'front-desk' }];
  const rec = await open(page, { found: { servers: 2, tray: true }, channels: two });
  await startWizard(page);
  const w = wizard(page);
  await expect(w.locator('#wiz-tray')).toContainText('This computer already uses the chat server Office (192.168.2.20). Use it?');
  await expect(w.locator('#wiz-list .chatlist li')).toHaveCount(2);
  await w.getByRole('button', { name: 'Paste a connection string' }).click();
  await w.locator('#chat-string').fill('hello');
  await w.getByRole('button', { name: 'Connect', exact: true }).click();
  await expect(w.locator('#wiz-error')).toHaveText('This is not a team chat connection string. It starts with bubbles://');
  await w.locator('#chat-string').fill('bubbles://192.168.2.20:28225/?token=bub_x&fp=' + fx.CHAT_FP);
  await w.getByRole('button', { name: 'Connect', exact: true }).click();

  await expect(w.getByRole('group', { name: 'Which channel should alerts go to?' })).toBeVisible();
  await expect(w.getByRole('radio')).toHaveCount(2);
  await expect(w.getByRole('button', { name: 'Use this channel' })).toBeDisabled();
  await w.getByRole('radio', { name: '#front-desk' }).check();
  await w.getByRole('button', { name: 'Use this channel' }).click();
  await expect(w.locator('#wiz-ready')).toContainText('Alerts will go to #front-desk.');
  const ch = rec.requests.filter((r) => r.path === '/api/bubbles/channel').at(-1);
  expect(JSON.parse(ch.body)).toEqual({ channelId: 'C2' });
  // The string never stays on the page.
  expect(await page.evaluate(() => document.querySelector('#chat-string').value)).toBe('');
  clean(rec, errors);
});

test('no channel yet: Check again once the admin added netdiag', async ({ page }) => {
  const rec = await open(page, { channels: [], pairStates: ['approved'] });
  await startWizard(page);
  const w = wizard(page);
  await w.getByRole('button', { name: 'Use this server: Office' }).click();
  await w.getByRole('button', { name: 'It matches' }).click();
  await expect(w.getByRole('button', { name: 'Check again' })).toBeVisible({ timeout: 10_000 });
  await expect(w.locator('#wiz-channel')).toContainText("netdiag isn't in any channel yet. Ask your chat admin to add netdiag to a channel, for example #netdiag-alerts.");
  await expect(w.getByRole('button', { name: 'Check again' })).toBeFocused();
  rec.channels = [fx.CHAT_CHANNEL];
  await w.getByRole('button', { name: 'Check again' }).click();
  await expect(w.locator('#wiz-ready')).toContainText('Alerts will go to #netdiag-alerts.');
  expect(rec.unmatched).toEqual([]);
});

test('denied says so; expired offers Try again', async ({ page }) => {
  const rec = await open(page, { pairStates: ['denied'] });
  await startWizard(page);
  const w = wizard(page);
  await w.getByRole('button', { name: 'Use this server: Office' }).click();
  await w.getByRole('button', { name: 'It matches' }).click();
  await expect(w.locator('#wiz-stopped')).toContainText('Your chat admin said no.', { timeout: 10_000 });
  await expect(w.getByRole('button', { name: 'Try again' })).toBeHidden();
  await w.getByRole('button', { name: 'Close' }).click();

  rec.pairStates = ['expired'];
  await settingsDlg(page).locator('#chat-connect').click();
  await wizard(page).getByRole('button', { name: 'Use this server: Office' }).click();
  await wizard(page).getByRole('button', { name: 'It matches' }).click();
  await expect(w.locator('#wiz-stopped')).toContainText('The request ran out of time after 10 minutes.', { timeout: 10_000 });
  rec.pairStates = ['pending'];
  await w.getByRole('button', { name: 'Try again' }).click();
  await expect(w.locator('#wiz-code')).toHaveText('123 456');
  expect(rec.chatCalls.filter((c) => c === 'POST /api/bubbles/pair')).toHaveLength(3);
  expect(rec.unmatched).toEqual([]);
});

test('test alert errors are shown in plain words', async ({ page }) => {
  const msg = "netdiag isn't allowed to post in #netdiag-alerts. Ask your chat admin to add netdiag to that channel.";
  const rec = await open(page, { chat: 'connected', testResult: { ok: false, error: msg, hint: 'not_allowed' } });
  await openSettings(page);
  const s = settingsDlg(page);
  await s.locator('#chat-test').click();
  await expect(s.locator('#chat-msg')).toHaveText(msg);
  rec.testResult = { ok: false, error: "netdiag can't reach the chat server.", hint: 'unreachable' };
  await s.locator('#chat-test').click();
  await expect(s.locator('#chat-msg')).toHaveText("netdiag can't reach the chat server.");
  rec.testResult = null;
  await s.locator('#chat-test').click();
  await expect(s.locator('#chat-msg')).toHaveText('Test alert sent to #netdiag-alerts and closed again. Check that it appeared in bubbles.');
  expect(rec.unmatched).toEqual([]);
});

const STATES = [
  { state: 'connected', icon: 'chat', title: 'Team chat: connected', word: 'Connected' },
  { state: 'unreachable', icon: 'chat-warn', title: /^Team chat: can't reach the server since \d\d:\d\d$/, word: /Can't reach the server since \d\d:\d\d$/ },
  { state: 'needs_reconnect', icon: 'chat-off', title: 'Team chat: needs reconnecting', word: 'Needs reconnecting' },
  { state: 'no_channel', icon: 'chat-off', title: 'Team chat: no channel', word: 'No channel' },
  { state: 'identity_changed', icon: 'chat-off', title: 'Team chat: server identity changed', word: 'Server identity changed' },
];

for (const st of STATES) {
  test(`status tool and section: ${st.state}`, async ({ page }) => {
    const rec = await open(page, { chat: st.state });
    const tool = page.locator('#chat-tool');
    await expect(tool).toBeVisible();
    await expect(tool).toHaveAttribute('title', st.title);
    await expect(tool.locator('.sr')).toHaveText(st.title);
    await expect(tool.locator('use')).toHaveAttribute('href', 'icons.svg#i-' + st.icon);
    const box = await tool.boundingBox();
    expect([Math.round(box.width), Math.round(box.height)]).toEqual([34, 34]);
    await tool.click();
    const s = settingsDlg(page);
    await expect(s).toHaveJSProperty('open', true);
    await expect(s.locator('#chat-status .hs')).toHaveText(st.word);
    await expect(s.locator('#chat-status use')).toHaveAttribute('href', 'icons.svg#i-' + st.icon);
    const focused = await page.evaluate(() => document.activeElement && document.activeElement.closest('#set-chat') !== null);
    expect(focused, 'focus is in the team chat section').toBe(true);
    await expect(s.locator('#chat-identity')).toBeVisible({ visible: st.state === 'identity_changed' });
    await expect(s.locator('#chat-reconnect')).toBeVisible({ visible: st.state === 'needs_reconnect' || st.state === 'identity_changed' });
    await expect(s.locator('#chat-watching')).toBeVisible();
    if (st.state === 'needs_reconnect') {
      await expect(s.locator('#chat-trouble')).toContainText('Your chat admin may see a second netdiag app; they can remove the old one in Admin > Apps.');
      await s.locator('#chat-reconnect').click();
      await expect(wizard(page).locator('#wiz-code')).toHaveText('123 456');
      const pair = rec.requests.find((r) => r.path === '/api/bubbles/pair');
      expect(JSON.parse(pair.body)).toEqual({ reconnect: true });
    }
    if (st.state === 'identity_changed') {
      await s.locator('#chat-reconnect').click();
      await expect(wizard(page).locator('#wiz-fp-start')).toHaveText('3F2A 9C10 77B4 E0D1');
      const insp = rec.requests.find((r) => r.path === '/api/bubbles/inspect');
      expect(JSON.parse(insp.body)).toEqual({ address: '192.168.2.20:28225', onboardPort: 28226 });
    }
    expect(rec.unmatched).toEqual([]);
  });
}

test('the three status-tool states have distinct glyphs', async () => {
  const svg = await fs.readFile(path.join(WEB_DIR, 'icons.svg'), 'utf8');
  const glyph = (id) => (svg.match(new RegExp('<symbol id="' + id + '"[^>]*>([\\s\\S]*?)</symbol>')) || [])[1];
  const g = ['i-chat', 'i-chat-warn', 'i-chat-off'].map(glyph);
  expect(g.every(Boolean)).toBe(true);
  expect(new Set(g).size).toBe(3);
});

test('Disconnect asks first', async ({ page }) => {
  const rec = await open(page, { chat: 'connected' });
  await openSettings(page);
  const s = settingsDlg(page);
  let text = '';
  page.once('dialog', (d) => { text = d.message(); d.dismiss(); });
  await s.locator('#chat-disconnect').click();
  await expect.poll(() => text).toContain('Disconnect from team chat?');
  expect(text).toContain('Your chat admin should also remove netdiag in bubbles under Admin > Apps.');
  expect(rec.chatCalls).not.toContain('DELETE /api/bubbles');
  page.once('dialog', (d) => d.accept());
  await s.locator('#chat-disconnect').click();
  await expect(s.locator('#chat-status')).toHaveText('Status: Not connected');
  await expect(s.locator('#chat-connect')).toBeFocused();
  await expect(page.locator('#chat-tool')).toBeHidden();
  expect(rec.chatCalls).toContain('DELETE /api/bubbles');
  expect(rec.unmatched).toEqual([]);
});

test('Change channel offers the channels and keeps the current one ticked', async ({ page }) => {
  const rec = await open(page, { chat: 'connected', channels: [fx.CHAT_CHANNEL, { id: 'C2', name: 'front-desk' }] });
  await openSettings(page);
  await settingsDlg(page).locator('#chat-change').click();
  const w = wizard(page);
  await expect(w.getByRole('radio', { name: '#netdiag-alerts' })).toBeChecked();
  await w.getByRole('radio', { name: '#front-desk' }).check();
  await w.getByRole('button', { name: 'Use this channel' }).click();
  await expect(w.locator('#wiz-ready')).toContainText('Alerts will go to #front-desk.');
  // Changing the channel does not touch the Send switch.
  expect(puts(rec)).toHaveLength(0);
  expect(rec.unmatched).toEqual([]);
});

test('Settings Save keeps the other sections when the chat switch changes', async ({ page }) => {
  const set = fx.settings();
  set.mcp.enabled = true;
  set.community = 's1te';
  set.alerts.cooldownSec = 600;
  set.chat = { enabled: true, minSeverity: 'warning' };
  const rec = await open(page, { chat: 'connected', settings: set });
  await openSettings(page);
  const s = settingsDlg(page);
  await expect(s.locator('#chat-pending')).toBeHidden();
  await s.locator('#chat-enabled').uncheck();
  await s.locator('#chat-level').selectOption('critical');
  await expect(s.locator('#chat-pending')).toHaveText('Takes effect when you click Save.');
  await s.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(s).toHaveJSProperty('open', false);
  const doc = JSON.parse(puts(rec).at(-1).body);
  expect(doc.chat).toEqual({ enabled: false, minSeverity: 'critical' });
  const { chat: _c, mcpUrl: _u, ...rest } = doc;
  const { chat: _c2, ...want } = set;
  expect(rest).toEqual(want);
  expect(rec.unmatched).toEqual([]);
});

test('a page with an old token reads no team chat state', async ({ page }) => {
  const rec = await open(page, { chat: 'connected', session: 'new-token', stale: true });
  await expect(page.locator('#session-off')).toBeVisible();
  expect(rec.unauthorized).toContain('GET /api/bubbles (X-Netdiag-Session: wrong)');
  await expect(page.locator('#chat-tool')).toBeHidden();
});

for (const theme of THEMES) {
  test(`team chat is accessible ${theme}`, async ({ page }, testInfo) => {
    await page.emulateMedia({ colorScheme: theme });
    const rec = await open(page, { chat: 'identity_changed' }, theme);
    await openSettings(page);
    await expect(settingsDlg(page).locator('#chat-identity')).toBeVisible();
    await runAxe(page, `chat-settings-identity-${theme}`, testInfo);
    await page.keyboard.press('Escape');
    rec.chat = fx.bubblesStatus('off');
    await openSettings(page);
    await settingsDlg(page).locator('#chat-connect').click();
    const w = wizard(page);
    await expect(w.locator('#wiz-list')).toBeVisible();
    await runAxe(page, `chat-wizard-list-${theme}`, testInfo);
    await w.getByRole('button', { name: 'Use this server: Office' }).click();
    await expect(w.locator('#wiz-fp-start')).toBeVisible();
    await runAxe(page, `chat-wizard-fingerprint-${theme}`, testInfo);
    await w.getByRole('button', { name: 'It matches' }).click();
    await expect(w.locator('#wiz-code')).toBeVisible();
    await runAxe(page, `chat-wizard-code-${theme}`, testInfo);
  });
}

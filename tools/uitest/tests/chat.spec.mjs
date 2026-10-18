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
// esc makes text a literal part of a RegExp.
const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

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

// The page reads the settings as it loads. A dialog opened before they come
// starts from the defaults (Send off), so a spec could see the unsaved notice.
async function openSettings(page) {
  await expect.poll(() => page.evaluate(() => {
    const el = document.querySelector('.shell');
    const data = el && el._x_dataStack ? el._x_dataStack[0] : null;
    return !!(data && data.settings);
  })).toBe(true);
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

  // Step 8 (D10): said plainly, with only the condition in bold, and how
  // watching starts.
  await expect(w.locator('#wiz-done-on')).toHaveText('netdiag sends alerts to #netdiag-alerts. It sends them only while it is watching this network: tick Live mode and press Scan, then leave netdiag open.');
  await expect(w.locator('#wiz-done-off')).toBeHidden();
  await expect(w.locator('#wiz-done p:visible strong')).toHaveText(['It sends them only while it is watching this network:']);
  await w.getByRole('button', { name: 'Close' }).click();

  // Back in Settings, connected.
  await expect(w).toHaveJSProperty('open', false);
  const s = settingsDlg(page);
  await expect(s).toHaveJSProperty('open', true);
  await expect(s.locator('#chat-status')).toHaveText('Status: Connected');
  await expect(s.locator('#chat-where')).toHaveText('Sending to #netdiag-alerts on Office (192.168.2.20)');
  await expect(s.locator('#chat-enabled')).toBeChecked();
  await expect(s.locator('#chat-level')).toHaveValue('warning');
  await expect(s.locator('#chat-watching')).toHaveText('Alerts go to team chat only while netdiag is watching this network. Tick Live mode and press Scan, then leave netdiag open.');
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
  // A firewall on any OS, not only Windows Firewall.
  await expect(w.locator('#wiz-none')).toHaveText('No chat server found automatically. A firewall on this computer or a separate Wi-Fi network can hide it.');
  await w.getByRole('button', { name: 'Type the address' }).click();
  await expect(w.locator('#chat-address')).toBeFocused();
  // The example port is not the usual one it says to leave out.
  await expect(w.locator('#chat-address-help')).toHaveText("Your chat admin can read it in bubbles under Admin > Overview. Add the port only if your chat admin says it isn't the usual one (28225), for example 192.168.1.20:30000.");
  await w.locator('#chat-address').fill('203.0.113.5');
  await w.getByRole('button', { name: 'Check this server' }).click();
  await expect(w.locator('#wiz-error')).toHaveText('Use the address of a chat server on the local network, for example 192.168.1.20 or office.local.');
  // The error leaves the step as it was: focus goes back to the field, not
  // to the page body.
  await expect(w.locator('#chat-address')).toBeFocused();
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
  // Office is the tray's server: it is not offered a second time, and the
  // tray card does not touch the list.
  await expect(w.locator('#wiz-list .chatlist li')).toHaveCount(1);
  await expect(w.locator('#wiz-list .chatlist li')).toHaveText(/Warehouse · 192\.168\.2\.21/);
  const gap = await page.evaluate(() => document.querySelector('.chatlist').getBoundingClientRect().top - document.querySelector('#wiz-tray').getBoundingClientRect().bottom);
  expect(gap).toBeGreaterThanOrEqual(8);
  await w.getByRole('button', { name: 'Paste a connection string' }).click();
  await w.locator('#chat-string').fill('hello');
  await w.getByRole('button', { name: 'Connect', exact: true }).click();
  await expect(w.locator('#wiz-error')).toHaveText('This is not a team chat connection string. It starts with bubbles://');
  await expect(w.locator('#chat-string')).toBeFocused();
  await w.locator('#chat-string').fill('bubbles://192.168.2.20:28225/?token=bub_x&fp=' + fx.CHAT_FP);
  await w.getByRole('button', { name: 'Connect', exact: true }).click();

  await expect(w.getByRole('group', { name: 'Which channel should alerts go to?' })).toBeVisible();
  // The question reads as a sentence, not in capitals.
  await expect(w.locator('#wiz-chan-q')).toHaveCSS('text-transform', 'none');
  await expect(w.locator('#wiz-chan-q')).toHaveText('Which channel should alerts go to?');
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

test('no channel yet: Look again once the admin added netdiag', async ({ page }) => {
  const rec = await open(page, { channels: [], pairStates: ['approved'] });
  await startWizard(page);
  const w = wizard(page);
  await w.getByRole('button', { name: 'Use this server: Office' }).click();
  await w.getByRole('button', { name: 'It matches' }).click();
  await expect(w.getByRole('button', { name: 'Look again' })).toBeVisible({ timeout: 10_000 });
  await expect(w.locator('#wiz-channel')).toContainText("netdiag isn't in any channel yet. Ask your chat admin to add netdiag to a channel, for example #netdiag-alerts.");
  await expect(w.getByRole('button', { name: 'Look again' })).toBeFocused();
  rec.channels = [fx.CHAT_CHANNEL];
  await w.getByRole('button', { name: 'Look again' }).click();
  await expect(w.locator('#wiz-ready')).toContainText('Alerts will go to #netdiag-alerts.');
  expect(rec.unmatched).toEqual([]);
});

// README step 4 lets the person close the wizard and wait for the chat
// admin: the new connection already turned Send on, so a channel picked
// later with Change channel sends, and Done says so truthfully.
test('no channel yet, Close, then Change channel: Send is on and Done says alerts go', async ({ page }) => {
  const rec = await open(page, { channels: [], pairStates: ['approved'] });
  await startWizard(page);
  const w = wizard(page);
  await w.getByRole('button', { name: 'Use this server: Office' }).click();
  await w.getByRole('button', { name: 'It matches' }).click();
  await expect(w.getByRole('button', { name: 'Look again' })).toBeVisible({ timeout: 10_000 });
  await expect.poll(() => puts(rec).length).toBe(1);
  expect(JSON.parse(puts(rec)[0].body).chat).toEqual({ enabled: true, minSeverity: 'warning' });
  await w.getByRole('button', { name: 'Close' }).click();
  await expect(w).toHaveJSProperty('open', false);

  rec.channels = [fx.CHAT_CHANNEL];
  const s = settingsDlg(page);
  await expect(s).toHaveJSProperty('open', true);
  await s.locator('#chat-change').click();
  await expect(w.locator('#wiz-ready')).toContainText('Alerts will go to #netdiag-alerts.', { timeout: 10_000 });
  await w.getByRole('button', { name: 'Finish' }).click();
  await expect(w.locator('#wiz-done-on')).toBeVisible();
  await expect(w.locator('#wiz-done-off')).toBeHidden();
  expect(puts(rec)).toHaveLength(1);
  expect(rec.unmatched).toEqual([]);
});

// Someone who turned Send off and changes the channel keeps it off: Done
// says nothing is sent yet and how to start, instead of promising alerts.
test('Change channel with Send off: Done says sending is off and how to turn it on', async ({ page }) => {
  const rec = await open(page, { chat: 'connected', channels: [fx.CHAT_CHANNEL, { id: 'C2', name: 'front-desk' }] });
  await openSettings(page);
  await settingsDlg(page).locator('#chat-change').click();
  const w = wizard(page);
  await w.getByRole('radio', { name: '#front-desk' }).check();
  await w.getByRole('button', { name: 'Use this channel' }).click();
  await w.getByRole('button', { name: 'Finish' }).click();
  await expect(w.locator('#wiz-done-off')).toHaveText('Alerts will go to #front-desk once you turn on Send alerts to team chat in Settings and click Save. netdiag sends them only while it is watching this network: tick Live mode and press Scan, then leave netdiag open.');
  await expect(w.locator('#wiz-done-on')).toBeHidden();
  expect(puts(rec)).toHaveLength(0);
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
  // A failure is the yellow box with its shape, not small grey text.
  await expect(s.locator('#chat-msg p')).toHaveClass('alert warn');
  rec.testResult = { ok: false, error: "netdiag can't reach the chat server.", hint: 'unreachable' };
  await s.locator('#chat-test').click();
  await expect(s.locator('#chat-msg')).toHaveText("netdiag can't reach the chat server.");
  await expect(s.locator('#chat-msg p')).toHaveClass('alert warn');
  rec.testResult = null;
  await s.locator('#chat-test').click();
  await expect(s.locator('#chat-msg')).toHaveText('Test alert sent to #netdiag-alerts and closed again. Check that it appeared in bubbles.');
  await expect(s.locator('#chat-msg p')).toHaveClass('hs hs-ok');
  await expect(s.locator('#chat-msg use')).toHaveAttribute('href', 'icons.svg#i-circle-check');
  expect(rec.unmatched).toEqual([]);
});

test('a failed test alert in the wizard is the yellow box; progress stays muted', async ({ page }) => {
  let release;
  const held = new Promise((r) => { release = r; });
  const rec = await open(page, { testResult: { ok: false, error: 'The chat server turned down the test alert. Ask your chat admin to check bubbles\' log.', hint: 'refused' } });
  await page.route('**/api/bubbles/test', async (route) => { await held; return route.fallback(); });
  await startWizard(page);
  const w = wizard(page);
  await w.getByRole('button', { name: 'Use this server: Office' }).click();
  await w.getByRole('button', { name: 'It matches' }).click();
  await expect(w.locator('#wiz-ready')).toBeVisible({ timeout: 10_000 });
  await w.getByRole('button', { name: 'Send test alert' }).click();
  await expect(w.locator('#wiz-test-msg p')).toHaveText('Sending a test alert...');
  await expect(w.locator('#wiz-test-msg p')).toHaveClass('sub');
  release();
  await expect(w.locator('#wiz-test-msg p')).toHaveText("The chat server turned down the test alert. Ask your chat admin to check bubbles' log.");
  await expect(w.locator('#wiz-test-msg p')).toHaveClass('alert warn');
  await expect(w.getByRole('button', { name: 'Send test alert' })).toBeFocused();
  expect(rec.unmatched).toEqual([]);
});

// Settings with Send alerts to team chat on (the fixtures' default is off).
const sendOn = () => ({ ...fx.settings(), chat: { enabled: true, minSeverity: 'warning' } });

// The status word's colour matches its box (yellow for what a button puts
// right, red only with the identity warning), and connected with Send off
// is neither green nor the sending shape (audit item 1). ctword is the word
// the header button shows next to its shape: every state but connected and
// sending says it in words, not only on hover (a status is a shape plus a
// word).
const STATES = [
  { name: 'connected', state: 'connected', send: true, icon: 'chat', title: 'Team chat: connected', word: 'Connected', cls: 'hs-ok', tool: 'cs-connected', ctword: '' },
  { name: 'connected, sending off', state: 'connected', send: false, icon: 'chat-paused', section: 'info', title: 'Team chat: connected, sending is off',
    word: 'Connected (sending is off)', cls: 'hs-info', tool: 'cs-paused', ctword: 'Chat: sending off' },
  { name: 'unreachable', state: 'unreachable', send: true, icon: 'chat-warn', title: /^Team chat: can't reach the server since \d\d:\d\d$/, word: /Can't reach the server since \d\d:\d\d$/, cls: 'hs-warn', tool: 'cs-unreachable',
    ctword: "Chat: can't reach" },
  { name: 'needs_reconnect', state: 'needs_reconnect', send: true, icon: 'chat-off', title: 'Team chat: needs reconnecting', word: 'Needs reconnecting', cls: 'hs-warn', tool: 'cs-needs_reconnect',
    ctword: 'Chat: reconnect' },
  { name: 'no_channel', state: 'no_channel', send: true, icon: 'chat-off', title: 'Team chat: no channel', word: 'No channel', cls: 'hs-warn', tool: 'cs-no_channel', ctword: 'Chat: no channel' },
  { name: 'identity_changed', state: 'identity_changed', send: true, icon: 'chat-off', title: 'Team chat: server identity changed', word: 'Server identity changed', cls: 'hs-fail', tool: 'cs-identity_changed',
    ctword: 'Chat: stopped' },
];

for (const st of STATES) {
  test(`status tool and section: ${st.name}`, async ({ page }) => {
    const rec = await open(page, { chat: st.state, settings: st.send ? sendOn() : fx.settings() });
    const tool = page.locator('#chat-tool');
    await expect(tool).toBeVisible();
    await expect(tool).toHaveAttribute('title', st.title);
    await expect(tool.locator('use')).toHaveAttribute('href', 'icons.svg#i-' + st.icon);
    await expect(tool).toHaveClass(new RegExp('\\b' + st.tool + '\\b'));
    const box = await tool.boundingBox();
    // The accessible name starts with the word the button shows (label in
    // name, so voice control finds it), then says the state in full.
    const full = typeof st.title === 'string' ? esc(st.title) : st.title.source.replace(/^\^|\$$/g, '');
    await expect(tool).toHaveAccessibleName(new RegExp('^' + (st.ctword ? esc(st.ctword) + '\\. ' : '') + full + '$'));
    if (st.ctword) {
      await expect(tool.locator('.ctword')).toBeVisible();
      await expect(tool.locator('.ctword')).toHaveText(st.ctword);
      expect(Math.round(box.height)).toBe(34);
      expect(box.width).toBeGreaterThan(34);
    } else {
      await expect(tool.locator('.ctword')).toBeHidden();
      expect([Math.round(box.width), Math.round(box.height)]).toEqual([34, 34]);
    }
    await tool.click();
    const s = settingsDlg(page);
    await expect(s).toHaveJSProperty('open', true);
    await expect(s.locator('#chat-status .hs')).toHaveText(st.word);
    await expect(s.locator('#chat-status .hs')).toHaveClass(new RegExp('\\b' + st.cls + '\\b'));
    await expect(s.locator('#chat-status use')).toHaveAttribute('href', 'icons.svg#i-' + (st.section || st.icon));
    if (st.cls === 'hs-warn' && st.state !== 'unreachable') await expect(s.locator('#chat-trouble')).toHaveClass('alert warn');
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
    if (st.state === 'no_channel') {
      // No channel was ever chosen: there is nothing to change yet.
      await expect(s.locator('#chat-trouble')).toHaveText("netdiag isn't posting anywhere yet. Click Choose a channel to pick where alerts go.");
      await expect(s.locator('#chat-choose')).toBeFocused();
      await expect(s.locator('#chat-change')).toBeHidden();
      await s.locator('#chat-choose').click();
      await expect(wizard(page).getByRole('heading', { level: 2 })).toHaveText('Choose a channel');
    }
    if (st.name === 'connected, sending off') {
      // Nothing is sent, so nothing says "Sending to".
      await expect(s.locator('#chat-where')).toHaveText('Alerts would go to #netdiag-alerts on Office (192.168.2.20). Turn on Send alerts to team chat and click Save.');
      await expect(s.locator('#chat-enabled')).not.toBeChecked();
    }
    if (st.name === 'connected') await expect(s.locator('#chat-where')).toHaveText('Sending to #netdiag-alerts on Office (192.168.2.20)');
    if (st.state === 'unreachable') {
      // Nothing can be sent while the server is away.
      await expect(s.locator('#chat-where')).toHaveText('Alerts go to #netdiag-alerts on Office (192.168.2.20)');
    }
    expect(rec.unmatched).toEqual([]);
  });
}

test('the four status-tool states have distinct glyphs', async () => {
  const svg = await fs.readFile(path.join(WEB_DIR, 'icons.svg'), 'utf8');
  const glyph = (id) => (svg.match(new RegExp('<symbol id="' + id + '"[^>]*>([\\s\\S]*?)</symbol>')) || [])[1];
  const g = ['i-chat', 'i-chat-paused', 'i-chat-warn', 'i-chat-off'].map(glyph);
  expect(g.every(Boolean)).toBe(true);
  expect(new Set(g).size).toBe(4);
});

// Saving Send alerts to team chat changes the status at once: the header
// tool and Settings follow the saved switch, not the connection alone.
test('the status follows the saved Send switch', async ({ page }) => {
  const rec = await open(page, { chat: 'connected', settings: sendOn() });
  const tool = page.locator('#chat-tool');
  await expect(tool).toHaveAttribute('title', 'Team chat: connected');
  await openSettings(page);
  const s = settingsDlg(page);
  await s.locator('#chat-enabled').uncheck();
  // Not saved yet: still sending.
  await expect(s.locator('#chat-status .hs')).toHaveText('Connected');
  await s.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(s).toHaveJSProperty('open', false);
  await expect(tool).toHaveAttribute('title', 'Team chat: connected, sending is off');
  await expect(tool.locator('use')).toHaveAttribute('href', 'icons.svg#i-chat-paused');
  await openSettings(page);
  await expect(s.locator('#chat-status .hs')).toHaveText('Connected (sending is off)');
  await expect(s.locator('#chat-where')).toContainText('Alerts would go to #netdiag-alerts');
  expect(rec.unmatched).toEqual([]);
});

test('Disconnect asks first in its own dialog: Disconnect or Cancel', async ({ page }) => {
  const rec = await open(page, { chat: 'connected' });
  await openSettings(page);
  const s = settingsDlg(page);
  const off = page.locator('#chat-off-dialog');
  await s.locator('#chat-disconnect').click();
  await expect(off).toHaveJSProperty('open', true);
  await expect(off.getByRole('heading', { name: 'Disconnect from team chat?' })).toBeVisible();
  await expect(off.locator('#chat-off-q')).toHaveText('netdiag stops sending alerts. Your chat admin should also remove netdiag in bubbles under Admin > Apps.');
  await expect(off.getByRole('button', { name: 'Cancel' })).toBeFocused();
  await expect(off.getByRole('button', { name: 'Disconnect' })).toHaveClass(/danger/);
  // Esc keeps the connection, closes only this dialog and gives focus back.
  await page.keyboard.press('Escape');
  await expect(off).toHaveJSProperty('open', false);
  await expect(s).toHaveJSProperty('open', true);
  await expect(s.locator('#chat-disconnect')).toBeFocused();
  // Cancel does the same.
  await s.locator('#chat-disconnect').click();
  await off.getByRole('button', { name: 'Cancel' }).click();
  await expect(off).toHaveJSProperty('open', false);
  await expect(s.locator('#chat-disconnect')).toBeFocused();
  expect(rec.chatCalls).not.toContain('DELETE /api/bubbles');

  await s.locator('#chat-disconnect').click();
  await off.getByRole('button', { name: 'Disconnect' }).click();
  await expect(off).toHaveJSProperty('open', false);
  await expect(s.locator('#chat-status')).toHaveText('Status: Not connected');
  await expect(s.locator('#chat-connect')).toBeFocused();
  // The confirmation is seen, and announced (role=status), in the "off" state.
  await expect(s.locator('#chat-msg')).toBeVisible();
  await expect(s.locator('#chat-msg')).toHaveText('Disconnected from team chat.');
  await expect(s.locator('#chat-msg')).toHaveAttribute('role', 'status');
  await expect(page.locator('#chat-tool')).toBeHidden();
  expect(rec.chatCalls).toContain('DELETE /api/bubbles');
  expect(rec.unmatched).toEqual([]);
});

test('a saved connection this computer cannot open says so, not that the admin removed netdiag', async ({ page }) => {
  const rec = await open(page, { chat: 'needs_reconnect', chatExtra: { reason: 'unreadable',
    lastError: "netdiag can't open its saved team chat key on this computer, so it has to be connected again." } });
  await openSettings(page);
  const s = settingsDlg(page);
  await expect(s.locator('#chat-status .hs')).toHaveText('Needs reconnecting');
  await expect(s.locator('#chat-trouble')).toHaveText("netdiag can't open its saved team chat connection on this computer (it may have been copied from another computer or Windows user), so it has to be connected again. Click Reconnect. Your chat admin may see a second netdiag app; they can remove the old one in Admin > Apps.");
  await expect(s.locator('#chat-where')).toBeHidden();
  await page.keyboard.press('Escape');
  await expect(s).toHaveJSProperty('open', false);
  // A token the server refused (401) keeps the 4.10 words.
  rec.chat = fx.bubblesStatus('needs_reconnect');
  await openSettings(page);
  await expect(s.locator('#chat-trouble')).toContainText('The chat server no longer accepts netdiag (your chat admin may have removed it). Click Reconnect.');
  expect(rec.unmatched).toEqual([]);
});

test('a channel that refuses netdiag: no "Sending to", and what to do', async ({ page }) => {
  const rec = await open(page, { chat: 'no_channel', chatExtra: { reason: 'not_allowed' }, settings: sendOn() });
  await expect(page.locator('#chat-tool')).toHaveAttribute('title', 'Team chat: no channel');
  await expect(page.locator('#chat-tool use')).toHaveAttribute('href', 'icons.svg#i-chat-off');
  await openSettings(page);
  const s = settingsDlg(page);
  await expect(s.locator('#chat-status .hs')).toHaveText('No channel');
  await expect(s.locator('#chat-where')).toBeHidden();
  await expect(s.locator('#chat-trouble')).toHaveText("netdiag isn't allowed to post in #netdiag-alerts any more. Ask your chat admin to add netdiag to it again, then send a test alert, or click Change channel.");
  // A test alert that gets through puts it right.
  await expect(s.locator('#chat-test')).toBeVisible();
  await expect(s.locator('#chat-change')).toBeVisible();
  await expect(s.locator('#chat-choose')).toBeHidden();
  await page.keyboard.press('Escape');
  rec.chat = fx.bubblesStatus('no_channel', { reason: 'archived' });
  await openSettings(page);
  await expect(s.locator('#chat-trouble')).toHaveText('The channel #netdiag-alerts is archived. Click Change channel to choose another one.');
  await page.keyboard.press('Escape');
  // A connection that works but could not be saved says so in any state.
  rec.chat = fx.bubblesStatus('connected', { unsaved: fx.CHAT_UNSAVED });
  await openSettings(page);
  await expect(s.locator('#chat-unsaved')).toHaveText(fx.CHAT_UNSAVED);
  await expect(s.locator('#chat-where')).toHaveText('Sending to #netdiag-alerts on Office (192.168.2.20)');
  expect(rec.unmatched).toEqual([]);
});

test('closing the wizard while netdiag asks the chat server cancels that request', async ({ page }) => {
  const rec = await open(page, { pairStates: ['approved'] });
  let release;
  const held = new Promise((r) => { release = r; });
  await page.route('**/api/bubbles/pair', async (route) => {
    if (route.request().method() === 'POST') await held;
    return route.fallback();
  });
  await startWizard(page);
  const w = wizard(page);
  await w.getByRole('button', { name: 'Use this server: Office' }).click();
  await w.getByRole('button', { name: 'It matches' }).click();
  await expect(w.getByRole('button', { name: 'It matches' })).toBeDisabled();
  await page.keyboard.press('Escape');
  await expect(w).toHaveJSProperty('open', false);
  await expect(settingsDlg(page).locator('#chat-connect')).toBeFocused();
  const cancels = () => rec.chatCalls.filter((c) => c === 'DELETE /api/bubbles/pair').length;
  await expect.poll(cancels).toBe(1);
  // The answer comes after the wizard closed: netdiag cancels it again once
  // it knows it went out, never waits for the admin, never connects and
  // never turns Send alerts on.
  release();
  await expect.poll(cancels).toBe(2);
  expect(rec.chatCalls.slice(-3)).toEqual(['DELETE /api/bubbles/pair', 'POST /api/bubbles/pair', 'DELETE /api/bubbles/pair']);
  await page.waitForTimeout(2500);
  expect(rec.requests.filter((r) => r.path === '/api/bubbles/pair/status')).toHaveLength(0);
  expect(puts(rec)).toHaveLength(0);
  await expect(w).toHaveJSProperty('open', false);
  await expect(page.locator('#chat-tool')).toBeHidden();
  expect(rec.unmatched).toEqual([]);
});

test('an approval with an error stops on the channel step and says so', async ({ page }) => {
  const msg = fx.CHAT_UNSAVED;
  const rec = await open(page, { pairError: msg });
  await startWizard(page);
  const w = wizard(page);
  await w.getByRole('button', { name: 'Use this server: Office' }).click();
  await w.getByRole('button', { name: 'It matches' }).click();
  await expect(w.locator('#wiz-error')).toHaveText(msg, { timeout: 10_000 });
  await expect(w.locator('#wiz-channel')).toBeVisible();
  await expect(w.locator('#wiz-ready')).toBeHidden();
  expect(puts(rec)).toHaveLength(0);
  // Choosing the channel saves it again; then the wizard goes on.
  await expect(w.getByRole('radio', { name: '#netdiag-alerts' })).toBeChecked();
  await w.getByRole('button', { name: 'Use this channel' }).click();
  await expect(w.locator('#wiz-ready')).toContainText('Alerts will go to #netdiag-alerts.');
  await expect(w.locator('#wiz-error')).toBeHidden();
  await expect.poll(() => puts(rec).length).toBe(1);
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

// M11 (4.10 Recovery): while the server is away the section says what
// will be sent when it is back (only the last hour's alerts: older posts
// are dropped, PostTTL) and offers Try now, which asks the server at once.
test('unreachable: what is sent when it is back, and Try now', async ({ page }) => {
  const errors = watchErrors(page);
  const rec = await open(page, { chat: 'unreachable', settings: sendOn() });
  await page.locator('#chat-tool').click();
  const s = settingsDlg(page);
  await expect(s.locator('#chat-trouble')).toHaveText(/^Can't reach the chat server since \d\d:\d\d\. netdiag keeps trying\. Alerts from the last hour are sent when it is back\.$/);
  const retry = s.locator('#chat-retry');
  await expect(retry).toBeVisible();
  await expect(retry).toBeFocused(); // the header tool's target while the server is away
  await expect(retry.locator('use')).toHaveAttribute('href', 'icons.svg#i-refresh-cw');

  await retry.click(); // still away
  await expect(s.locator('#chat-msg')).toHaveText("Still can't reach the chat server. netdiag keeps trying.");
  await expect(s.locator('#chat-msg p')).toHaveClass('alert warn');
  await expect(retry).toBeFocused();
  rec.retryTo = 'connected';
  await retry.click();
  await expect(s.locator('#chat-msg')).toHaveText('The chat server answers again.');
  await expect(s.locator('#chat-status .hs')).toHaveText('Connected');
  await expect(retry).toBeHidden();
  await expect(s.locator('#chat-test')).toBeFocused();
  await expect(page.locator('#chat-tool use')).toHaveAttribute('href', 'icons.svg#i-chat');
  expect(rec.chatCalls.filter((c) => c === 'POST /api/bubbles/retry')).toHaveLength(2);
  clean(rec, errors);
});

// With Send alerts off nothing waits to be sent (switching it off dropped
// what waited), so the words promise nothing.
test('unreachable with Send off promises nothing', async ({ page }) => {
  await open(page, { chat: 'unreachable' });
  await page.locator('#chat-tool').click();
  const s = settingsDlg(page);
  await expect(s.locator('#chat-trouble')).toHaveText(/^Can't reach the chat server since \d\d:\d\d\. netdiag keeps trying\.$/);
  await expect(s.locator('#chat-where')).toHaveText('Alerts would go to #netdiag-alerts on Office (192.168.2.20). Turn on Send alerts to team chat and click Save.');
});

// A button's answer is about the state it was given in: once the status
// moves on, the yellow box goes instead of contradicting it, and Settings
// opened again starts without the last time's answers.
test('an old Try now or test alert answer goes once the state changes', async ({ page }) => {
  const errors = watchErrors(page);
  const rec = await open(page, { chat: 'unreachable', settings: sendOn() });
  const loadChat = () => page.evaluate(() => window.Alpine.$data(document.querySelector('.shell')).loadChat());
  await page.locator('#chat-tool').click();
  const s = settingsDlg(page);
  const msg = s.locator('#chat-msg p');
  await s.locator('#chat-retry').click(); // still away
  await expect(msg).toHaveText("Still can't reach the chat server. netdiag keeps trying.");
  await expect(msg).toHaveClass('alert warn');
  await loadChat(); // the same state: the answer stays
  await expect(msg).toBeVisible();
  // The server is back by itself: the next status read clears the box.
  rec.chat = fx.bubblesStatus('connected');
  await loadChat();
  await expect(s.locator('#chat-status .hs')).toHaveText('Connected');
  await expect(s.locator('#chat-where')).toHaveText('Sending to #netdiag-alerts on Office (192.168.2.20)');
  await expect(msg).toBeHidden();

  // A refused test alert stays while nothing changes, and is gone when
  // Settings opens again.
  const refused = "The chat server turned down the test alert. Ask your chat admin to check bubbles' log.";
  rec.testResult = { ok: false, error: refused, hint: 'refused' };
  await s.locator('#chat-test').click();
  await expect(msg).toHaveText(refused);
  await expect(msg).toHaveClass('alert warn');
  await loadChat();
  await expect(msg).toHaveText(refused);
  await page.keyboard.press('Escape');
  await expect(s).toHaveJSProperty('open', false);
  await page.locator('#chat-tool').click();
  await expect(s).toHaveJSProperty('open', true);
  await expect(msg).toBeHidden();

  // A good answer goes too once the server is away again.
  rec.testResult = null;
  await s.locator('#chat-test').click();
  await expect(msg).toHaveClass('hs hs-ok');
  rec.chat = fx.bubblesStatus('unreachable');
  await loadChat();
  await expect(s.locator('#chat-retry')).toBeVisible();
  await expect(msg).toBeHidden();
  clean(rec, errors);
});

// A Disconnect netdiag could not finish says why, in the server's plain
// words, in the yellow box, and the connection's buttons stay.
test('a failed Disconnect says why in the yellow box', async ({ page }) => {
  const errors = watchErrors(page);
  const words = "netdiag couldn't remove the team chat connection from its data folder. Check that the data folder isn't read-only, then click Disconnect again.";
  const rec = await open(page, { chat: 'connected', settings: sendOn() });
  await page.route('**/api/bubbles', (route) => route.request().method() === 'DELETE'
    ? route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: words }) })
    : route.fallback());
  await openSettings(page);
  const s = settingsDlg(page);
  await s.locator('#chat-disconnect').click();
  await page.locator('#chat-off-dialog').getByRole('button', { name: 'Disconnect' }).click();
  await expect(s.locator('#chat-msg')).toHaveText(words);
  await expect(s.locator('#chat-msg p')).toHaveClass('alert warn');
  await expect(s.locator('#chat-status .hs')).toHaveText('Connected');
  await expect(s.locator('#chat-disconnect')).toBeVisible();
  await expect(page.locator('#chat-tool')).toBeVisible();
  clean(rec, errors);
});

// The status carries the Send switch netdiag applies (sendAlerts), and it
// wins over this page's copy of the settings: another page (netdiag opens
// a new one on every launch) may have saved it since.
test("the status's Send switch wins over this page's settings", async ({ page }) => {
  const errors = watchErrors(page);
  const rec = await open(page, { chat: 'connected', settings: sendOn(), chatExtra: { sendAlerts: false } });
  const tool = page.locator('#chat-tool');
  await expect(tool).toHaveAttribute('title', 'Team chat: connected, sending is off');
  await expect(tool.locator('use')).toHaveAttribute('href', 'icons.svg#i-chat-paused');
  await openSettings(page);
  const s = settingsDlg(page);
  await expect(s.locator('#chat-status .hs')).toHaveText('Connected (sending is off)');
  await expect(s.locator('#chat-status .hs')).toHaveClass(/\bhs-info\b/);
  await expect(s.locator('#chat-where')).toHaveText('Alerts would go to #netdiag-alerts on Office (192.168.2.20). Turn on Send alerts to team chat and click Save.');
  clean(rec, errors);
});

test('Send switched off on another page: this page follows', async ({ page }) => {
  const errors = watchErrors(page);
  const rec = await open(page, { chat: 'connected', settings: sendOn() });
  const tool = page.locator('#chat-tool');
  await expect(tool).toHaveAttribute('title', 'Team chat: connected');
  await openSettings(page);
  const s = settingsDlg(page);
  await expect(s.locator('#chat-where')).toHaveText('Sending to #netdiag-alerts on Office (192.168.2.20)');
  await page.keyboard.press('Escape');
  // Another page saves Send off; this page's own copy is not touched.
  await page.evaluate(async () => {
    const d = window.Alpine.$data(document.querySelector('.shell'));
    const doc = JSON.parse(JSON.stringify(d.settings));
    delete doc.mcpUrl;
    doc.chat.enabled = false;
    await d.api('PUT', '/api/settings', doc);
  });
  expect(await page.evaluate(() => window.Alpine.$data(document.querySelector('.shell')).settings.chat.enabled)).toBe(true);
  // Settings reads the status as it opens: nothing claims sending, and
  // the switch shows what was saved, with nothing waiting for Save.
  await openSettings(page);
  await expect(s.locator('#chat-status .hs')).toHaveText('Connected (sending is off)');
  await expect(s.locator('#chat-where')).toContainText('Alerts would go to #netdiag-alerts');
  await expect(tool).toHaveAttribute('title', 'Team chat: connected, sending is off');
  await expect(s.locator('#chat-enabled')).not.toBeChecked();
  await expect(s.locator('#chat-pending')).toBeHidden();
  clean(rec, errors);
});

// Settings that could not be read never hide that alerts are being sent.
test('a failed settings read does not hide that alerts are sent', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan', chat: 'connected', settings: sendOn() });
  await page.route('**/api/settings', (route) => route.request().method() === 'GET'
    ? route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'Could not read the settings.' }) })
    : route.fallback());
  await page.goto(HOME);
  const tool = page.locator('#chat-tool');
  await expect(tool).toHaveAttribute('title', 'Team chat: connected');
  await expect(tool.locator('use')).toHaveAttribute('href', 'icons.svg#i-chat');
  expect(rec.unmatched).toEqual([]);
});

// What the outbox and the polling say ("Team chat server moved to…",
// "The chat server turned down an alert. …") reaches the Activity log under Notice,
// once each, also when the status is read again.
test("team chat's notes reach the Activity log once", async ({ page }) => {
  const errors = watchErrors(page);
  const notes = [{ id: 1, at: '2026-10-17T10:40:00Z', text: 'Team chat server moved to 192.168.1.27.' }];
  const rec = await open(page, { chat: 'connected', chatExtra: { activity: notes } });
  await page.locator('#tab-activity').click();
  const log = page.getByRole('log', { name: 'Activity log' });
  await expect(log).toContainText('Team chat server moved to 192.168.1.27.');
  rec.chat = fx.bubblesStatus('connected', { activity: [...notes, { id: 2, at: '2026-10-17T10:41:00Z', text: "The chat server turned down an alert. Ask your chat admin to check bubbles' log." }] });
  await page.evaluate(() => window.Alpine.$data(document.querySelector('.shell')).loadChat());
  await expect(log).toContainText("The chat server turned down an alert. Ask your chat admin to check bubbles' log.");
  await page.evaluate(() => window.Alpine.$data(document.querySelector('.shell')).loadChat());
  await expect(log.getByText('Team chat server moved to 192.168.1.27.')).toHaveCount(1);
  await page.locator('#lf-notice').click(); // the Notice chip hides them
  await expect(log).not.toContainText('Team chat server moved');
  clean(rec, errors);
});

// The page stays open while netdiag restarts: the new run's notes start at
// id 1 again (its run says so), and none of them is taken for one the page
// saw from the old run, even when there are more of them by the next read.
test("team chat's notes after netdiag restarted all reach the Activity log", async ({ page }) => {
  const errors = watchErrors(page);
  const note = (id, text) => ({ id, at: '2026-10-17T10:4' + id + ':00Z', text });
  const old = [1, 2, 3, 4, 5].map((id) => note(id, `Old run note ${id}.`));
  const rec = await open(page, { chat: 'connected', chatExtra: { activity: old } });
  await page.locator('#tab-activity').click();
  const log = page.getByRole('log', { name: 'Activity log' });
  await expect(log).toContainText('Old run note 5.');
  const fresh = [1, 2, 3, 4, 5, 6].map((id) => note(id, `New run note ${id}.`));
  rec.chat = fx.bubblesStatus('connected', { activity: fresh, run: '0b7e44c19a3d2f60' });
  await page.evaluate(() => window.Alpine.$data(document.querySelector('.shell')).loadChat());
  for (const n of fresh) await expect(log.getByText(n.text)).toHaveCount(1);
  await page.evaluate(() => window.Alpine.$data(document.querySelector('.shell')).loadChat());
  await expect(log.getByText('New run note 1.')).toHaveCount(1);
  await expect(log.getByText('Old run note 1.')).toHaveCount(1);
  clean(rec, errors);
});

// Every step a person can reach by mistake, or that found nothing, has a
// visible way out (no Esc key on a touch screen): Cancel goes back to
// Settings and its Connect button.
test('every wizard step has Cancel', async ({ page }) => {
  let release;
  const held = new Promise((r) => { release = r; });
  const rec = await open(page, { found: { servers: 0 } });
  await page.route('**/api/bubbles/discover', async (route) => { await held; return route.fallback(); });
  const cancel = async (step) => {
    const w = wizard(page);
    await expect(w.locator('#wiz-' + step)).toBeVisible();
    await w.locator('#wiz-' + step).getByRole('button', { name: 'Cancel' }).click();
    await expect(w).toHaveJSProperty('open', false);
    await expect(settingsDlg(page)).toHaveJSProperty('open', true);
    await expect(settingsDlg(page).locator('#chat-connect')).toBeFocused();
  };
  await startWizard(page);
  // While netdiag looks for the server.
  await expect(wizard(page).locator('#wiz-find')).toContainText('Looking for your team chat on the local network…');
  await cancel('find');
  release();
  // Nothing found.
  await settingsDlg(page).locator('#chat-connect').click();
  await expect(wizard(page).locator('#wiz-none')).toBeVisible();
  await cancel('list');
  // Typing the address, and pasting a connection string.
  for (const [button, step] of [['Type the address', 'address'], ['Paste a connection string', 'string']]) {
    await settingsDlg(page).locator('#chat-connect').click();
    await wizard(page).getByRole('button', { name: button }).click();
    await cancel(step);
  }
  // Is this the right server?
  await settingsDlg(page).locator('#chat-connect').click();
  await wizard(page).getByRole('button', { name: 'Type the address' }).click();
  await wizard(page).locator('#chat-address').fill('192.168.2.20');
  await wizard(page).getByRole('button', { name: 'Check this server' }).click();
  await cancel('fingerprint');
  expect(rec.chatCalls).not.toContain('POST /api/bubbles/pair');
  expect(rec.unmatched).toEqual([]);
});

// The fingerprint step names the server as Settings does (no usual port),
// and a refusal there gives focus back to It matches.
test('the fingerprint step: the server as Settings names it, and focus after an error', async ({ page }) => {
  const rec = await open(page);
  await page.route('**/api/bubbles/pair', (route) => route.request().method() === 'POST'
    ? route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ error: "The chat server turned down netdiag's request. Ask your chat admin to check bubbles' log." }) })
    : route.fallback());
  await startWizard(page);
  const w = wizard(page);
  await w.getByRole('button', { name: 'Use this server: Office' }).click();
  await expect(w.locator('#wiz-fingerprint')).toContainText('Server: Office (192.168.2.20). This computer: ' + fx.SELF_IP);
  await expect(w.locator('#wiz-fingerprint')).not.toContainText('28225');
  await w.getByRole('button', { name: 'It matches' }).click();
  await expect(w.locator('#wiz-error')).toHaveText("The chat server turned down netdiag's request. Ask your chat admin to check bubbles' log.");
  await expect(w.getByRole('button', { name: 'It matches' })).toBeFocused();
  expect(rec.unmatched).toEqual([]);
});

// The approval page is for the chat admin, and may warn about bubbles' own
// certificate on this computer. Its longer label wraps at 390 px instead
// of running out of the dialog.
test('the approval page link says who it is for', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const rec = await open(page);
  await startWizard(page);
  const w = wizard(page);
  await w.getByRole('button', { name: 'Use this server: Office' }).click();
  await w.getByRole('button', { name: 'It matches' }).click();
  await expect(w.locator('#wiz-admin')).toHaveText('Open the approval page (for your chat admin) (opens in a new tab)');
  await expect(w.locator('#wiz-admin-help')).toHaveText('If the browser warns about the connection, ask your chat admin to approve on their own computer.');
  await expect(w.locator('#wiz-admin')).toHaveAttribute('aria-describedby', 'wiz-admin-help');
  // toHaveText passes on a hidden link too: measure only once the approve
  // step shows.
  await expect(w.locator('#wiz-code')).toHaveText('123 456');
  await expect(w.locator('#wiz-admin')).toBeVisible();
  await expect(w.locator('#wiz-admin-help')).toBeVisible();
  const link = await w.locator('#wiz-admin').boundingBox(), box = await w.locator('.sform').boundingBox();
  expect(link.x).toBeGreaterThanOrEqual(box.x);
  expect(link.x + link.width).toBeLessThanOrEqual(box.x + box.width);
  await page.keyboard.press('Escape');
  expect(rec.unmatched).toEqual([]);
});

// Reconnect and Change channel say what they do, in the heading and while
// netdiag waits, instead of "Connect to team chat" and "Looking for…".
test('Reconnect and Change channel have their own heading and wait text', async ({ page }) => {
  let release;
  const held = new Promise((r) => { release = r; });
  const rec = await open(page, { chat: 'needs_reconnect', settings: sendOn(), channels: [fx.CHAT_CHANNEL, { id: 'C2', name: 'front-desk' }] });
  await page.route('**/api/bubbles/pair', async (route) => { if (route.request().method() === 'POST') await held; return route.fallback(); });
  await openSettings(page);
  await settingsDlg(page).locator('#chat-reconnect').click();
  const w = wizard(page);
  await expect(w.getByRole('heading', { level: 2 })).toHaveText('Reconnect to team chat');
  await expect(w.locator('#wiz-find')).toHaveText(/Asking Office \(192\.168\.2\.20\) to let netdiag in again…/);
  await expect(w.locator('#wiz-find')).not.toContainText('Looking for');
  release();
  await expect(w.locator('#wiz-code')).toHaveText('123 456');
  await page.keyboard.press('Escape');
  await expect(w).toHaveJSProperty('open', false);

  let release2;
  const held2 = new Promise((r) => { release2 = r; });
  rec.chat = fx.bubblesStatus('connected');
  await page.route('**/api/bubbles/channel', async (route) => { await held2; return route.fallback(); });
  await page.evaluate(() => window.Alpine.$data(document.querySelector('.shell')).loadChat());
  await settingsDlg(page).locator('#chat-change').click();
  await expect(w.getByRole('heading', { level: 2 })).toHaveText('Change channel');
  await expect(w.locator('#wiz-find')).toHaveText(/Checking which channels netdiag is in…/);
  release2();
  await expect(w.getByRole('radio', { name: '#netdiag-alerts' })).toBeChecked();
  expect(rec.unmatched).toEqual([]);
});

// At 390 px the channel question is one line with no border through it.
test('the channel question fits at 390 px', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const rec = await open(page, { chat: 'connected', channels: [fx.CHAT_CHANNEL, { id: 'C2', name: 'front-desk' }] });
  await openSettings(page);
  await settingsDlg(page).locator('#chat-change').click();
  const q = wizard(page).locator('#wiz-chan-q');
  await expect(q).toBeVisible();
  const { lines, border } = await q.evaluate((el) => {
    const cs = getComputedStyle(el), lh = parseFloat(cs.lineHeight);
    const line = isNaN(lh) ? parseFloat(cs.fontSize) * 1.3 : lh;
    return { lines: Math.round(el.getBoundingClientRect().height / line), border: getComputedStyle(el.closest('fieldset')).borderTopStyle };
  });
  expect(lines).toBe(1);
  expect(border).toBe('none');
  expect(rec.unmatched).toEqual([]);
});

for (const theme of THEMES) {
  test(`team chat is accessible ${theme}`, async ({ page }, testInfo) => {
    await page.emulateMedia({ colorScheme: theme });
    const rec = await open(page, { chat: 'identity_changed' }, theme);
    await openSettings(page);
    await expect(settingsDlg(page).locator('#chat-identity')).toBeVisible();
    await runAxe(page, `chat-settings-identity-${theme}`, testInfo);
    rec.chat = fx.bubblesStatus('unreachable');
    await page.evaluate(() => window.Alpine.$data(document.querySelector('.shell')).loadChat());
    await expect(settingsDlg(page).locator('#chat-retry')).toBeVisible();
    await runAxe(page, `chat-settings-unreachable-${theme}`, testInfo);
    await settingsDlg(page).locator('#chat-retry').click();
    await expect(settingsDlg(page).locator('#chat-msg p')).toHaveClass('alert warn');
    await runAxe(page, `chat-settings-retry-failed-${theme}`, testInfo);
    rec.chat = fx.bubblesStatus('connected'); // Send is off in these settings
    await page.evaluate(() => window.Alpine.$data(document.querySelector('.shell')).loadChat());
    await expect(page.locator('#chat-tool')).toHaveClass(/cs-paused/);
    await runAxe(page, `chat-settings-sending-off-${theme}`, testInfo);
    await settingsDlg(page).locator('#chat-disconnect').click();
    await expect(page.locator('#chat-off-dialog')).toHaveJSProperty('open', true);
    await runAxe(page, `chat-disconnect-${theme}`, testInfo);
    await page.keyboard.press('Escape');
    await expect(page.locator('#chat-off-dialog')).toHaveJSProperty('open', false);
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

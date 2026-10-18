// F7: settings that were not valid in the settings file are reset when
// netdiag reads it, and Settings says so once, naming the fields.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME } from '../support/mock.mjs';
import * as fx from '../fixtures/index.mjs';
import { runAxe } from '../support/axe.mjs';
import { THEMES } from '../support/config.mjs';

// Team chat is not set up, so its controls do not show yet: the notice
// names its level once, and says where it will be, without listing the
// fields twice.
const NOTE = 'Some settings were not valid and were reset to their defaults: Supply threshold, Team chat alert level. Check them below. ' +
  'Team chat settings show here once team chat is set up.';

// Settings that are not the defaults in every section the dialog sends
// back, so a save that rebuilt a section from defaults would show. The
// repaired fields hold the defaults the server reset them to.
function custom() {
  const s = fx.settings();
  s.community = 'frontdesk';
  s.alerts.cooldownSec = 600;
  s.alerts.triggers.newDevice = false;
  s.ignoredConflictMACs = ['AA:BB:CC:DD:EE:01'];
  s.mcp = { enabled: true };
  s.chat = { enabled: true, minSeverity: 'warning' };
  return s;
}

async function openSettings(page) {
  const menu = page.locator('.menubtn');
  if (await menu.isVisible() && (await menu.getAttribute('aria-expanded')) !== 'true') await menu.click();
  await page.locator('#nav-settings').click();
}

for (const theme of THEMES) {
  test(`repaired settings notice ${theme}`, async ({ page }, testInfo) => {
    await initStorage(page, { theme, live: false });
    const rec = await installMocks(page, { scenario: 'scan', settings: custom(), repaired: ['alerts.supplyThresholdPct', 'chat.minSeverity'] });
    await page.goto(HOME);
    const dialog = page.locator('#settings-dialog');
    const note = dialog.locator('#settings-repaired');
    const nav = page.locator('#nav-settings');

    await nav.click();
    await expect(dialog).toBeVisible();
    await expect(note).toBeVisible();
    await expect(note).toHaveText(NOTE);
    await expect(note).toHaveAttribute('role', 'status');
    // A shape plus a word: the triangle comes before the text, inside it.
    await expect(note.locator('svg.ico use')).toHaveAttribute('href', 'icons.svg#i-triangle-alert');
    expect(await note.evaluate((el) => getComputedStyle(el, '::before').content)).toBe('none');
    // The reset field shows its default.
    await expect(dialog.getByLabel('Supply threshold (%, 1-100)')).toHaveValue('15');
    await runAxe(page, `settings-repaired-${theme}`, testInfo);

    // Esc closes Settings and returns focus; the notice does not come back.
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
    await expect(nav).toBeFocused();
    await nav.click();
    await expect(dialog).toBeVisible();
    await expect(note).toBeHidden();

    // Saving sends every section back unchanged, without the repaired list.
    await dialog.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(dialog).toBeHidden();
    const put = rec.requests.filter((r) => r.method === 'PUT' && r.path === '/api/settings').pop();
    const doc = JSON.parse(put.body);
    const want = custom();
    expect(doc.repaired).toBeUndefined();
    expect(doc.alerts.supplyThresholdPct).toBe(15);
    expect(doc.community).toBe(want.community);
    expect(doc.alerts).toEqual(want.alerts);
    expect(doc.ignoredConflictMACs).toEqual(want.ignoredConflictMACs);
    expect(doc.mcp).toEqual(want.mcp);
    expect(doc.chat).toEqual(want.chat);
    expect(rec.unmatched).toEqual([]);
    expect(rec.external).toEqual([]);
  });
}

// A save made before Settings is opened (here Mute alerts in the header)
// writes a valid file, so GET no longer reports the field; the page still
// shows the notice it was given when it loaded.
test('a save before Settings opens keeps the notice', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan', settings: custom(), repaired: ['alerts.cooldownSec'] });
  await page.goto(HOME);
  await page.getByRole('button', { name: 'Mute alerts' }).click();
  await expect.poll(() => rec.requests.filter((r) => r.method === 'PUT' && r.path === '/api/settings').length).toBe(1);
  const mute = JSON.parse(rec.requests.find((r) => r.method === 'PUT').body);
  expect(mute.repaired).toBeUndefined();
  expect(mute.chat).toEqual(custom().chat);
  await openSettings(page);
  const note = page.locator('#settings-repaired');
  await expect(note).toHaveText('Some settings were not valid and were reset to their defaults: Repeat-alert cooldown. Check them below.');
});

test('no repaired settings, no notice', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { scenario: 'scan' });
  await page.goto(HOME);
  await page.locator('#nav-settings').click();
  const dialog = page.locator('#settings-dialog');
  await expect(dialog).toBeVisible();
  await expect(dialog.locator('#settings-repaired')).toBeHidden();
});

test('reset fields with no control in Settings are named only', async ({ page }) => {
  await initStorage(page, { theme: 'light', live: false });
  await installMocks(page, { scenario: 'scan', repaired: ['webServer.trustedCert', 'chat'] });
  await page.goto(HOME);
  await openSettings(page);
  await expect(page.locator('#settings-repaired')).toHaveText('Some settings were not valid and were reset to their defaults: Trusted certificate, Team chat. Team chat settings show here once team chat is set up.');
});

test('unknown repaired field is named by its path', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { scenario: 'scan', repaired: ['alerts.cooldownSec', 'someNewField'] });
  await page.setViewportSize({ width: 360, height: 800 });
  await page.goto(HOME);
  await openSettings(page);
  const note = page.locator('#settings-repaired');
  await expect(note).toHaveText('Some settings were not valid and were reset to their defaults: Repeat-alert cooldown, someNewField. Check them below.');
  const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(over).toBeLessThanOrEqual(0);
  const d = await page.locator('#settings-dialog').evaluate((el) => el.scrollWidth - el.clientWidth);
  expect(d).toBeLessThanOrEqual(0);
});

// Once team chat is set up its level shows in Settings, under the label the
// notice names.
test('with team chat set up, its reset level is checked below under the same name', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { scenario: 'scan', chat: 'connected', repaired: ['chat.minSeverity'] });
  await page.goto(HOME);
  await expect(page.locator('#chat-tool')).toBeVisible();
  await openSettings(page);
  await expect(page.locator('#settings-repaired')).toHaveText('Some settings were not valid and were reset to their defaults: Team chat alert level. Check them below.');
  await expect(page.locator('#settings-dialog').getByLabel('Team chat alert level')).toHaveValue('warning');
});

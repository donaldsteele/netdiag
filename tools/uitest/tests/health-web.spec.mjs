// Web server health card: the "set the URL" link when none is set, and
// trusting a self-signed certificate.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage } from '../support/mock.mjs';
import { runAxe } from '../support/axe.mjs';
import { THEMES } from '../support/config.mjs';
import * as fx from '../fixtures/index.mjs';

const webCard = (page) => page.locator('.hcard').filter({ has: page.getByRole('heading', { name: 'Web server' }) });
const fpLong = (fp) => fp.toUpperCase().match(/../g).join(':');
const settingsPuts = (rec) => rec.requests.filter((r) => r.method === 'PUT' && r.path === '/api/settings');

test('web server card offers Open Settings when no URL is set', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.goto('/');
  await page.locator('#tab-health').click();
  const card = webCard(page);
  await expect(card).toContainText('What this means: No web server is set, so this check is skipped.');
  await expect(card).toContainText('not configured');
  // The advice's Open Settings button replaces the v0.5.7 link.
  await expect(page.locator('.hcard a.setweb:visible')).toHaveCount(0);
  await card.getByRole('button', { name: /^Open Settings/ }).click();
  const dialog = page.locator('#settings-dialog');
  await expect(dialog).toHaveJSProperty('open', true);
  await expect(dialog.getByLabel('Web server URL')).toBeFocused();
  await expect(dialog.locator('#trusted-cert')).toBeHidden();
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

test('a saved report without advice still links to Settings when no URL is set', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan', web: 'unset-old' });
  await page.goto('/');
  await page.locator('#tab-health').click();
  const card = webCard(page);
  await expect(card).toContainText('not configured');
  // Other cards never offer the link.
  await expect(page.locator('.hcard a.setweb:visible')).toHaveCount(1);
  const link = card.getByRole('link', { name: 'Set the web server URL in Settings' });
  await expect(link).toBeVisible();
  await link.click();
  const dialog = page.locator('#settings-dialog');
  await expect(dialog).toHaveJSProperty('open', true);
  await expect(dialog.getByLabel('Web server URL')).toBeVisible();
  await expect(dialog.locator('#trusted-cert')).toBeHidden();
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

test('web server card hides the link once a URL is set', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { scenario: 'scan', web: 'ok' });
  await page.goto('/');
  await page.locator('#tab-health').click();
  const card = webCard(page);
  await expect(card).toContainText('HTTP 200');
  await expect(card.getByRole('link', { name: 'Set the web server URL in Settings' })).toBeHidden();
  await expect(card.getByRole('button', { name: 'Trust this certificate' })).toBeHidden();
});

for (const theme of THEMES) {
  test(`trust a self-signed web server certificate ${theme}`, async ({ page }, testInfo) => {
    await initStorage(page, { theme, live: false });
    const rec = await installMocks(page, { scenario: 'scan', web: 'untrusted' });
    await page.goto('/');
    await page.locator('#tab-health').click();
    const card = webCard(page);
    await expect(card).toContainText('the certificate is not trusted');
    const trust = card.getByRole('button', { name: 'Trust this certificate' });
    await expect(trust).toBeVisible();
    await expect(page.getByRole('button', { name: 'Trust this certificate' })).toHaveCount(1);
    // The advice points at that button, not at Settings (which can't trust).
    await expect(card.locator('.advice ol.steps li').first()).toHaveText(
      'If this is your own server on this network, open Health in netdiag and press Trust this certificate on the Web server card.');
    await expect(card.getByRole('button', { name: /^Open Settings/ })).toHaveCount(0);
    await runAxe(page, `health-untrusted-${theme}`, testInfo);

    // Saying no saves nothing.
    const messages = [];
    page.once('dialog', (d) => { messages.push(d.message()); d.dismiss(); });
    await trust.click();
    await expect.poll(() => messages.length).toBe(1);
    expect(settingsPuts(rec)).toHaveLength(0);
    expect(messages[0]).toContain(fx.WEB_URL);
    expect(messages[0]).toContain(fpLong(fx.CERT_SHA256));
    expect(messages[0]).toContain('Only do this for your own server');

    page.once('dialog', (d) => d.accept());
    await trust.click();
    await expect(card).toContainText('HTTP 200');
    await expect(trust).toBeHidden();
    const puts = settingsPuts(rec);
    expect(puts).toHaveLength(1);
    expect(puts[0].contentType).toBe('application/json');
    const doc = JSON.parse(puts[0].body);
    expect(doc.webServer).toMatchObject({ url: fx.WEB_URL, trustedCert: fx.CERT_SHA256, expectStatus: 200 });
    // The whole document is sent, so the other sections stay as they were.
    const base = fx.settings();
    expect(doc.alerts).toEqual(base.alerts);
    expect(doc.mcp).toEqual(base.mcp);
    expect(doc.community).toBe(base.community);
    expect(doc.ignoredConflictMACs).toEqual(base.ignoredConflictMACs);
    // The checks run again after the save.
    const order = rec.requests.map((r) => r.method + ' ' + r.path);
    expect(order.lastIndexOf('POST /api/health/run')).toBeGreaterThan(order.indexOf('PUT /api/settings'));

    // Settings show the trusted certificate (short form) and can forget it.
    await page.locator('#nav-settings').click();
    const dialog = page.locator('#settings-dialog');
    const row = dialog.locator('#trusted-cert');
    await expect(row).toBeVisible();
    await expect(row).toContainText(fpLong(fx.CERT_SHA256).slice(0, 23) + '…');
    await expect(dialog.locator('#trusted-pending')).toBeHidden();
    await runAxe(page, `settings-trusted-${theme}`, testInfo);
    await row.getByRole('button', { name: 'Forget the trusted certificate' }).click();
    await expect(row).toBeHidden();
    await expect(dialog.locator('#trusted-pending')).toBeVisible();
    await dialog.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(dialog).toHaveJSProperty('open', false);
    expect(JSON.parse(settingsPuts(rec).pop().body).webServer.trustedCert).toBe('');
    expect(rec.unmatched, 'unmatched requests').toEqual([]);
    expect(rec.external, 'external requests').toEqual([]);
  });
}

test('a trusted certificate is kept for the same server and dropped for another', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan', web: 'untrusted' });
  await page.goto('/');
  await page.locator('#tab-health').click();
  page.once('dialog', (d) => d.accept());
  await webCard(page).getByRole('button', { name: 'Trust this certificate' }).click();
  await expect(webCard(page)).toContainText('HTTP 200');

  const dialog = page.locator('#settings-dialog');
  const save = async (url) => {
    await page.locator('#nav-settings').click();
    await dialog.getByLabel('Web server URL').fill(url);
    await dialog.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(dialog).toHaveJSProperty('open', false);
    return JSON.parse(settingsPuts(rec).pop().body).webServer.trustedCert;
  };
  expect(await save(fx.WEB_URL + 'status')).toBe(fx.CERT_SHA256);
  expect(await save('https://other.local/')).toBe('');
});

test('the trust button is only offered for the web server address in Settings', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  // A result for an address that is no longer the one in Settings.
  const rec = await installMocks(page, { scenario: 'scan', web: 'untrusted', webURL: 'https://old-nas.local/' });
  await page.goto('/');
  await page.locator('#tab-health').click();
  const card = webCard(page);
  await expect(card).toContainText('the certificate is not trusted');
  await expect(card.getByRole('button', { name: 'Trust this certificate' })).toHaveCount(0);
  await expect(page.locator('.trustbtn:visible')).toHaveCount(0);
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

test('the trust button is offered for an address with a password', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const base = fx.settings();
  const url = 'https://admin:pa55word@intranet.local';
  const rec = await installMocks(page, { scenario: 'scan', web: 'untrusted',
    settings: { ...base, webServer: { ...base.webServer, url } } });
  await page.goto('/');
  await page.locator('#tab-health').click();
  const card = webCard(page);
  // The server reports the address with the password hidden.
  const trust = card.getByRole('button', { name: 'Trust this certificate' });
  await expect(trust).toBeVisible();
  const messages = [];
  page.once('dialog', (d) => { messages.push(d.message()); d.dismiss(); });
  await trust.click();
  await expect.poll(() => messages.length).toBe(1);
  expect(messages[0]).toContain('https://admin:xxxxx@intranet.local');
  expect(messages[0]).not.toContain('pa55word');
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

test('saving a new web server address or certificate runs the checks again', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan', web: 'untrusted' });
  await page.goto('/');
  await page.locator('#tab-health').click();
  const card = webCard(page);
  await expect(card.getByRole('button', { name: 'Trust this certificate' })).toBeVisible();
  const runs = () => rec.requests.filter((r) => r.method === 'POST' && r.path === '/api/health/run').length;
  const dialog = page.locator('#settings-dialog');
  const save = async (edit) => {
    await page.locator('#nav-settings').click();
    await expect(dialog).toHaveJSProperty('open', true);
    await edit();
    await dialog.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(dialog).toHaveJSProperty('open', false);
  };

  // Saved with the same web server settings: no new check.
  let before = runs();
  await save(() => dialog.getByLabel('Web server URL').fill(fx.WEB_URL));
  expect(settingsPuts(rec)).toHaveLength(1);
  expect(runs()).toBe(before);

  // A new address: the checks run again after the save, for that address.
  before = runs();
  await save(() => dialog.getByLabel('Web server URL').fill('https://nas.local/'));
  await expect.poll(runs).toBe(before + 1);
  const order = rec.requests.map((r) => r.method + ' ' + r.path);
  expect(order.lastIndexOf('POST /api/health/run')).toBeGreaterThan(order.lastIndexOf('PUT /api/settings'));
  await expect(card.getByRole('button', { name: 'Trust this certificate' })).toBeVisible();

  // Trusting, then forgetting the certificate: checked again each time.
  page.once('dialog', (d) => d.accept());
  await card.getByRole('button', { name: 'Trust this certificate' }).click();
  await expect(card).toContainText('HTTP 200');
  before = runs();
  await save(() => dialog.locator('#trusted-cert').getByRole('button', { name: 'Forget the trusted certificate' }).click());
  await expect.poll(runs).toBe(before + 1);
  await expect(card).toContainText('the certificate is not trusted');
  expect(rec.unmatched, 'unmatched requests').toEqual([]);
});

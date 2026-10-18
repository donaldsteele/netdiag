// Problems right now (v0.6 #1, M8). The card reads GET /api/problems
// (internal/problems NewView). The golden file testdata/problems/hosts.json
// is shared with the Go tests: the page's hostIssues gives every case's
// issues (TestHostIssuesGolden checks problems.HostIssues against the same
// file), and the card items of its hosts are what problems.Current gives
// (TestCurrentGolden).
import fs from 'node:fs';
import path from 'node:path';
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME } from '../support/mock.mjs';
import { runAxe } from '../support/axe.mjs';
import { scanToFinish } from '../support/steps.mjs';
import { THEMES, UITEST_DIR } from '../support/config.mjs';
import * as fx from '../fixtures/index.mjs';

const GOLDEN = JSON.parse(fs.readFileSync(path.resolve(UITEST_DIR, '..', '..', 'testdata', 'problems', 'hosts.json'), 'utf8'));

const card = (page) => page.locator('#problems-now');
const items = (page) => card(page).locator('.pitem');
const problemsTab = (page) => page.locator('.tabs button.tab', { hasText: /^Problems \(/ });
const fineLine = /OK Everything looks fine \(checked \d\d:\d\d\)/;

// A golden host as the page receives it: every field a host event carries.
function wire(h) {
  return { isSelf: false, alive: false, foundBy: [], rttMs: 0, mac: '', vendor: '', hostname: '', openPorts: [], web: [],
    snmp: null, isPrinter: false, printerReason: '', printer: null, errors: [], updatedAt: '2026-10-17T10:42:00Z', online: false,
    mdns: null, deviceType: 'unknown', ...h };
}

test('the page and problems.HostIssues agree on every golden case', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page);
  await page.goto(HOME);
  await expect(card(page).locator('#pn-fine')).toBeVisible();
  for (const c of GOLDEN.cases) {
    const got = await page.evaluate(({ hosts, conflicts, pct }) => {
      const a = window.Alpine.$data(document.querySelector('.shell'));
      a.conflicts = conflicts;
      a.settings = { ...a.settings, alerts: { ...a.settings.alerts, supplyThresholdPct: pct } };
      const out = {};
      for (const h of hosts) {
        const is = a.hostIssues(h).map((x) => x.kind);
        if (is.length) out[h.ip] = is;
        if (a.isProblem(h) !== is.length > 0) out[h.ip + ' isProblem'] = a.isProblem(h);
      }
      return out;
    }, { hosts: GOLDEN.hosts.map(wire), conflicts: GOLDEN.conflicts, pct: c.supplyThresholdPct });
    expect(got, c.name).toEqual(c.issues);
  }
});

test('an offline phone is on the Problems tab but not on the card', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const list = GOLDEN.hosts.filter((h) => h.ip === '10.0.1.1' || h.ip === '10.0.1.2').map(wire);
  const frames = [...list.map((h) => ({ type: 'host', data: h })),
    { type: 'done', data: { ...fx.scanResult({ list }), conflicts: [], notices: [] } }];
  // problems.Current gives nothing for these two devices with the default
  // alert types (TestEverythingFine), so the card is fine.
  const s = fx.settings();
  const rec = await installMocks(page, { frames, problems: 'fine',
    settings: { ...s, alerts: { ...s.alerts, triggers: { ...s.alerts.triggers, deviceOffline: false } } } });
  await page.goto(HOME);
  await page.getByRole('button', { name: 'Scan', exact: true }).click();
  await expect(page.locator('section.progress .stage')).toHaveText('Finished');
  await expect(card(page).locator('#pn-fine')).toContainText(fineLine);
  await expect(card(page)).not.toContainText('10.0.1.2');
  await expect(problemsTab(page)).toHaveText('Problems (1)');
  await problemsTab(page).click();
  await expect(page.locator('#hosts tbody tr.host')).toHaveCount(1);
  await expect(page.locator('#hosts tbody tr.host')).toContainText('10.0.1.2');
  expect(rec.unmatched).toEqual([]);
});

for (const theme of THEMES) {
  test(`everything fine: OK with the time, a note, and the not-watching line (${theme})`, async ({ page }, testInfo) => {
    await initStorage(page, { theme, live: false });
    const rec = await installMocks(page);
    await page.goto(HOME);
    const fine = card(page).locator('#pn-fine');
    await expect(fine).toContainText(fineLine);
    await expect(fine.locator('use')).toHaveAttribute('href', 'icons.svg#i-circle-check');
    await expect(card(page).getByRole('heading', { name: 'Problems right now' })).toBeVisible();
    // The weak Wi-Fi of the fixture report is a note, not a problem.
    const notes = card(page).locator('#pn-notes');
    await expect(notes.getByRole('heading', { name: 'Also worth knowing' })).toBeVisible();
    await expect(notes.locator('.pitem')).toHaveCount(1);
    await expect(notes.locator('.psev')).toHaveText('Note');
    await expect(notes.locator('.psev use')).toHaveAttribute('href', 'icons.svg#i-info');
    await expect(card(page).locator('#pn-watch')).toHaveText(
      /^netdiag isn't watching right now\. This is from the last check at \d\d:\d\d\. Tick Live mode and press Scan to keep watching\.$/);
    await runAxe(page, `problems-fine-${theme}`, testInfo);
    expect(rec.problemsCalls).toBeGreaterThan(0);
    expect(rec.unmatched).toEqual([]);
  });
}

test('with "This network has no internet" on, the no-internet network is fine on the card (D15)', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { health: 'offline', settings: { ...fx.settings(), noInternetNetworks: [fx.NETWORK] } });
  await page.goto(HOME);
  await expect(card(page).locator('#pn-fine')).toContainText(fineLine);
  await expect(items(page)).toHaveCount(0);
});

// Six items in three severities (a dead router, four warnings from the
// fixture scan, a note), as GET /api/problems sends them.
function someView(extra = {}) {
  const all = fx.healthAll().checks;
  return fx.problemsView({ open: [...fx.problemItems(), ...fx.healthProblems({ checks: [all[5], all[0]] })], ...extra });
}

for (const theme of THEMES) {
  test(`items in severity order with a shape and a word; Show all expands inline (${theme})`, async ({ page }, testInfo) => {
    await initStorage(page, { theme, live: false });
    const rec = await installMocks(page, { problems: someView() });
    await page.goto(HOME);
    await expect(items(page)).toHaveCount(5);
    await expect(card(page).locator('#pn-fine')).toBeHidden();
    const words = await card(page).locator('.psev').allTextContents();
    expect(words).toEqual(['Problem', 'Warning', 'Warning', 'Warning', 'Warning']);
    const icons = await card(page).locator('.psev use').evaluateAll((els) => els.map((e) => e.getAttribute('href')));
    expect(icons[0]).toBe('icons.svg#i-circle-x');
    expect(icons[1]).toBe('icons.svg#i-triangle-alert');
    await expect(items(page).first()).toContainText('Health check failed');
    await expect(items(page).first()).toContainText('Gateway reachable');
    await expect(items(page).nth(2)).toContainText('Black Toner at 15% · BRW001BA9123456 (192.168.2.10)');
    const all = card(page).locator('#pn-all');
    await expect(all).toHaveText('Show all 6');
    await expect(all).toHaveAttribute('aria-expanded', 'false');
    await all.click();
    await expect(items(page)).toHaveCount(6);
    await expect(card(page).locator('#pn-notes .psev')).toHaveText('Note');
    await expect(all).toHaveText('Show fewer');
    await expect(all).toHaveAttribute('aria-expanded', 'true');
    await expect(all).toBeFocused();

    // What to try opens the advice; Esc closes it and returns to its summary.
    const tryIt = items(page).nth(2).locator('details.ptry');
    await tryIt.locator('summary').click();
    await expect(tryIt.locator('.advice')).toContainText('What this means: Black Toner is at 15%.');
    await page.keyboard.press('Escape');
    await expect(tryIt).toHaveJSProperty('open', false);
    await expect(tryIt.locator('summary')).toBeFocused();
    await runAxe(page, `problems-some-${theme}`, testInfo);

    // Open Health goes to the health checks.
    await items(page).first().getByRole('button', { name: /^Open Health/ }).click();
    await expect(page.locator('#tab-health')).toHaveAttribute('aria-current', 'page');
    await expect(page.locator('#health')).toBeFocused();
    expect(rec.unmatched).toEqual([]);
  });
}

test('Show device opens the device in the drawer', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { problems: 'some' });
  await page.goto(HOME);
  // Before the scan the page lists no devices, so there is nothing to show.
  await expect(items(page).filter({ hasText: 'Printer alert' }).getByRole('button', { name: /^Show device/ })).toBeHidden();
  await scanToFinish(page);
  const brother = items(page).filter({ hasText: 'Printer alert' });
  await brother.getByRole('button', { name: /^Show device/ }).click();
  await expect(page.locator('#drawer')).toBeVisible();
  await expect(page.locator('#drawer')).toContainText('192.168.2.10');
  await page.keyboard.press('Escape');
  await expect(page.locator('#drawer')).toBeHidden();
  expect(rec.unmatched).toEqual([]);
});

test('an OK notice brings a Fixed item; Dismiss removes it', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: true });
  const rec = await installMocks(page, { scenario: 'live', problems: 'fine' });
  // The live run ends with the device's OK; the card reads again 2 s after
  // that burst of events, once, and the ledger has it fixed by then.
  rec.frames = [...fx.liveFrames(), { type: 'notice', data: fx.fixedNotice(2) }];
  await page.goto(HOME);
  await expect(card(page).locator('#pn-fine')).toBeVisible();
  const before = rec.problemsCalls;
  rec.problems = fx.problemsView({ fixed: fx.fixedItems(), source: 'live', watching: true });
  await page.getByRole('button', { name: 'Scan', exact: true }).click();
  await expect(page.locator('#alerts .banner.sev-ok')).toHaveCount(1);
  const fixed = card(page).locator('#pn-fixed');
  await expect(fixed.getByRole('heading', { name: 'Fixed today' })).toBeVisible();
  await expect(fixed.locator('.pitem')).toHaveCount(2);
  const first = fixed.locator('.pitem').first();
  await expect(first.locator('.psev')).toHaveText('Fixed');
  await expect(first.locator('.psev use')).toHaveAttribute('href', 'icons.svg#i-circle-check');
  await expect(first).toContainText(/Fixed\s+at \d\d:\d\d \(was down 12 min\): Device back online/);
  await expect(first).toContainText('192.168.2.200');
  // netdiag is watching: no "isn't watching" line.
  await expect(card(page).locator('#pn-watch')).toBeHidden();
  await page.waitForTimeout(2500);
  expect(rec.problemsCalls - before).toBeLessThanOrEqual(2);

  await first.getByRole('button', { name: /^Dismiss/ }).click();
  await expect(fixed.locator('.pitem')).toHaveCount(1);
  await expect(card(page).locator('#pn-h')).toBeFocused();
  // A dismissed item stays dismissed when the card reads again.
  await page.evaluate(() => window.Alpine.$data(document.querySelector('.shell')).loadProblems());
  await expect(fixed.locator('.pitem')).toHaveCount(1);
  expect(rec.unmatched).toEqual([]);
});

test('netdiag has looked at nothing yet', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { problems: { source: 'none', watching: false, everythingFine: false, open: [], fixed: [] } });
  await page.goto(HOME);
  await expect(card(page).locator('#pn-none')).toContainText('Not checked');
  await expect(card(page).locator('#pn-fine')).toBeHidden();
  await expect(card(page).locator('#pn-watch')).toBeHidden();
});

test('the health checks have not answered yet', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { problems: fx.problemsView({ hasHealth: false, source: 'live', watching: true }) });
  await page.goto(HOME);
  await expect(card(page).locator('#pn-quiet')).toContainText('No problems found so far.');
  await expect(card(page).locator('#pn-fine')).toBeHidden();
});

for (const width of [360, 390, 768, 1100, 1440]) {
  test(`the expanded card adds no inner scroller or sideways scroll at ${width}px`, async ({ page }) => {
    await initStorage(page, { theme: 'dark', live: false });
    const view = someView({ fixed: fx.fixedItems() });
    // A long description wraps instead of widening the page.
    view.open[1] = { ...view.open[1], detail: 'A very long supply description that goes on and on '.repeat(3) };
    await installMocks(page, { problems: view });
    await page.setViewportSize({ width, height: 800 });
    await page.goto(HOME);
    await card(page).locator('#pn-all').click();
    await expect(items(page)).toHaveCount(8);
    for (const d of await card(page).locator('details.ptry').all()) {
      if (await d.isVisible()) await d.locator('summary').click();
    }
    const scrollers = await page.evaluate(() => [...document.querySelectorAll('#problems-now, #problems-now *')].filter((el) => {
      const s = getComputedStyle(el);
      return ['auto', 'scroll'].includes(s.overflowY) && el.scrollHeight > el.clientHeight + 1;
    }).map((el) => el.tagName + '.' + el.className));
    expect(scrollers).toEqual([]);
    expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0);
  });
}

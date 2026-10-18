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
    const got = await page.evaluate(({ hosts, conflicts, pct, ignored }) => {
      const a = window.Alpine.$data(document.querySelector('.shell'));
      a.conflicts = conflicts;
      a.settings = { ...a.settings, ignoredConflictMACs: ignored, alerts: { ...a.settings.alerts, supplyThresholdPct: pct } };
      const out = {};
      for (const h of hosts) {
        const is = a.hostIssues(h).map((x) => x.kind);
        if (is.length) out[h.ip] = is;
        if (a.isProblem(h) !== is.length > 0) out[h.ip + ' isProblem'] = a.isProblem(h);
      }
      return out;
    }, { hosts: GOLDEN.hosts.map(wire), conflicts: GOLDEN.conflicts, pct: c.supplyThresholdPct, ignored: c.ignoredConflictMACs || [] });
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
    // The OK line gives the time already, and Live mode is not ticked.
    await expect(card(page).locator('#pn-watch')).toHaveText(
      /^netdiag isn't watching right now\. Tick Live mode and press Scan to start watching\.$/);
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

const someView = fx.someProblemsView;

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
    // The check's short name, as the health strip says it: "Gateway
    // reachable" next to "Health check failed" reads as if it answered.
    await expect(items(page).first().locator('.pline .sub')).toHaveText('Gateway');
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

test('an OK notice alone makes the card read again 2 s later, once', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: true });
  const rec = await installMocks(page, { scenario: 'live', problems: 'fine' });
  // A printer's cover closing gives only the status loop's OK notice: no
  // presence, health or conflict event comes with it.
  rec.frames = [{ type: 'notice', data: fx.fixedNotice(2) }];
  await page.goto(HOME);
  await expect(card(page).locator('#pn-fine')).toBeVisible();
  await expect.poll(() => rec.problemsCalls).toBeGreaterThan(0);
  const before = rec.problemsCalls;
  rec.problems = fx.problemsView({ fixed: fx.fixedItems(), source: 'live', watching: true });
  await page.getByRole('button', { name: 'Scan', exact: true }).click();
  await expect.poll(() => rec.sseAt.length).toBe(1);
  await expect(card(page).locator('#pn-fixed .pitem')).toHaveCount(2, { timeout: 10_000 });
  const reads = rec.problemsAt.slice(before);
  expect(reads).toHaveLength(1);
  expect(reads[0] - rec.sseAt[0]).toBeGreaterThanOrEqual(1500);
  await page.waitForTimeout(2500);
  expect(rec.problemsCalls - before).toBe(1);
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
  await expect(fixed.getByRole('heading', { name: 'Fixed in the last day' })).toBeVisible();
  await expect(fixed.locator('.pitem')).toHaveCount(2);
  const first = fixed.locator('.pitem').first();
  await expect(first.locator('.psev')).toHaveText('Fixed');
  await expect(first.locator('.psev use')).toHaveAttribute('href', 'icons.svg#i-circle-check');
  // A minute ago: today, or yesterday just after midnight.
  await expect(first).toContainText(/Fixed\s+at (?:(?:Sun|Mon|Tue|Wed|Thu|Fri|Sat) )?\d\d:\d\d \(was down 12 min\): Device back online/);
  await expect(first).toContainText('192.168.2.200');
  // A fixed check says which one it was.
  const dns = fixed.locator('.pitem').nth(1);
  await expect(dns.locator('.ptitle')).toHaveText(/: Health check OK again$/);
  await expect(dns.locator('.pline .sub')).toHaveText('DNS');
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

// No scan yet, but Check again ran: the card speaks for the health checks
// (their problems) and says the devices are not checked yet, never that
// netdiag looked at nothing.
for (const theme of THEMES) {
  test(`only the health checks ran (${theme})`, async ({ page }, testInfo) => {
    await initStorage(page, { theme, live: false });
    const gw = fx.healthProblems({ checks: [fx.healthAll().checks[0]] });
    const rec = await installMocks(page, { problems: fx.problemsView({ source: 'health', open: gw }) });
    await page.goto(HOME);
    const h = card(page).locator('#pn-health');
    await expect(h).toHaveText(/^\s*Not checked The health checks ran \(checked \d\d:\d\d\), but netdiag has not looked at the devices yet\. Press Scan to check them too\.$/);
    await expect(h.locator('use')).toHaveAttribute('href', 'icons.svg#i-circle-help');
    await expect(card(page).locator('#pn-none')).toBeHidden();
    await expect(card(page).locator('#pn-fine')).toBeHidden();
    await expect(card(page).locator('#pn-watch')).toBeHidden();
    await expect(items(page)).toHaveCount(1);
    await expect(items(page).first().locator('.psev')).toHaveText('Problem');
    await expect(items(page).first().locator('.pline .sub')).toHaveText('Gateway');
    await runAxe(page, `problems-health-${theme}`, testInfo);
    expect(rec.unmatched).toEqual([]);
  });
}

// The not-watching line says what to do: with Live mode ticked only press
// Scan, and it never repeats the time the line above gives.
test('the not-watching line with Live mode ticked says Press Scan', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: true });
  await installMocks(page);
  await page.goto(HOME);
  await expect(card(page).locator('#pn-fine')).toBeVisible();
  await expect(card(page).locator('#pn-watch')).toHaveText("netdiag isn't watching right now. Press Scan to start watching.");
  await page.locator('#live-mode').uncheck();
  await expect(card(page).locator('#pn-watch')).toHaveText("netdiag isn't watching right now. Tick Live mode and press Scan to start watching.");
});

test('the health checks have not answered yet', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { problems: fx.problemsView({ hasHealth: false, source: 'live', watching: true }) });
  await page.goto(HOME);
  await expect(card(page).locator('#pn-quiet')).toContainText('No problems found so far. netdiag has not finished checking yet.');
  await expect(card(page).locator('#pn-fine')).toBeHidden();
  await expect(card(page).locator('#pn-notrun')).toBeHidden();
});

// A finished scan without a health report (cancelled before its checks, or
// a saved scan without them): the checks will not finish, so the card says
// they did not run and offers the Health panel.
for (const source of ['latest', 'saved']) {
  test(`a ${source} scan whose health checks did not run`, async ({ page }) => {
    await initStorage(page, { theme: 'dark', live: false });
    const rec = await installMocks(page, { problems: fx.problemsView({ hasHealth: false, source }) });
    await page.goto(HOME);
    const nr = card(page).locator('#pn-notrun');
    await expect(nr).toContainText('Not checked No problems found, but the health checks did not run for this scan.');
    await expect(nr.locator('.pnnote use')).toHaveAttribute('href', 'icons.svg#i-circle-help');
    await expect(card(page).locator('#pn-quiet')).toBeHidden();
    await expect(card(page).locator('#pn-watch')).toBeVisible();
    await nr.getByRole('button', { name: 'Open Health to run the health checks' }).click();
    await expect(page.locator('#tab-health')).toHaveAttribute('aria-current', 'page');
    await expect(page.locator('#health')).toBeFocused();
    expect(rec.unmatched).toEqual([]);
  });
}

// The storm guard holds (v0.6-D36): the card never says everything is fine
// then, says why with a shape and a word, and gives the time of the last
// check, as the live pill does.
for (const theme of THEMES) {
  test(`while the storm guard holds the card says why (${theme})`, async ({ page }, testInfo) => {
    await initStorage(page, { theme, live: false });
    const lastCheck = new Date(Date.now() - 5 * 60000).toISOString();
    const rec = await installMocks(page, { problems: fx.problemsView({ source: 'live', watching: true, checkedAt: lastCheck,
      held: true, heldReason: 'link', heldSince: new Date(Date.now() - 4 * 60000).toISOString() }) });
    await page.goto(HOME);
    const held = card(page).locator('#pn-held');
    // Minutes ago: today, or yesterday just after midnight.
    const ago = String.raw`(?:(?:Sun|Mon|Tue|Wed|Thu|Fri|Sat) )?\d\d:\d\d`;
    await expect(held).toHaveText(new RegExp(String.raw`^\s*Paused This computer lost the local network \(since ${ago}\), so netdiag paused its checks\. This is from the last check at ${ago}\.\s*$`));
    await expect(held.locator('use')).toHaveAttribute('href', 'icons.svg#i-triangle-alert');
    await expect(card(page).locator('#pn-fine')).toBeHidden();
    await expect(card(page).locator('#pn-watch')).toBeHidden();
    await runAxe(page, `problems-held-${theme}`, testInfo);

    rec.problems = fx.problemsView({ source: 'live', watching: true, checkedAt: lastCheck, held: true, heldReason: 'network_changed',
      heldSince: lastCheck, open: fx.problemItems() });
    await page.evaluate(() => window.Alpine.$data(document.querySelector('.shell')).loadProblems());
    await expect(held).toHaveText(new RegExp(String.raw`^\s*Stopped This computer is on a different network now, so netdiag stopped its alerts\. This is from the last check of the network it was watching, at ${ago}\.\s*$`));
    // What it found before stays listed.
    await expect(items(page)).toHaveCount(4);
    // The Health panel's line agrees: it does not say everything is fine.
    expect(await page.evaluate(() => window.Alpine.$data(document.querySelector('.shell')).healthFineText())).toMatch(/^All health checks look fine/);
    expect(rec.unmatched).toEqual([]);
  });
}

// The card can speak for a saved scan of days ago: its times then name the
// day; a fix from yesterday is in the last day, not "today".
test('times of an earlier day name the day', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const day = 86400000;
  const old = new Date(Date.now() - 10 * day).toISOString();
  const yesterday = new Date(Date.now() - day).toISOString();
  const fixed = [{ ...fx.fixedItems()[0], openedAt: new Date(Date.parse(yesterday) - 12 * 60000).toISOString(), fixedAt: yesterday }];
  const open = [{ ...fx.problemItems()[0], since: yesterday }];
  await installMocks(page, { problems: fx.problemsView({ source: 'saved', checkedAt: old, open, fixed }) });
  await page.goto(HOME);
  const want = await page.evaluate(({ old, yesterday }) => {
    const t = (x) => new Date(x).toTimeString().slice(0, 5);
    const o = new Date(old), y = new Date(yesterday);
    return { old: o.getDate() + ' ' + ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'][o.getMonth()] + ' ' + t(old),
      yesterday: ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'][y.getDay()] + ' ' + t(yesterday) };
  }, { old, yesterday });
  await expect(card(page).locator('#pn-watch')).toContainText("netdiag isn't watching right now. This is from the last check at " + want.old + '.');
  await expect(items(page).first()).toContainText('since ' + want.yesterday);
  const fixedGroup = card(page).locator('#pn-fixed');
  await expect(fixedGroup.getByRole('heading', { name: 'Fixed in the last day' })).toBeVisible();
  await expect(fixedGroup.locator('.pitem')).toContainText('Fixed at ' + want.yesterday + ' (was down 12 min): Device back online');
});

// Ignore this MAC: the card reads again, so a conflict the person ignored
// leaves it (netdiag drops it for a finished scan too, problems.Current).
test('ignoring a MAC makes the card read again', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { problems: 'some' });
  await page.goto(HOME);
  await scanToFinish(page);
  await expect(items(page).filter({ hasText: 'Possible conflict' })).toHaveCount(1);
  rec.problems = fx.problemsView({ open: fx.problemItems().filter((p) => p.kind !== 'ipConflict') });
  page.once('dialog', (d) => d.accept());
  const before = rec.problemsCalls;
  await page.evaluate(() => window.Alpine.$data(document.querySelector('.shell')).ignoreMac('00:1B:A9:AA:00:30'));
  await expect.poll(() => rec.problemsCalls).toBeGreaterThan(before);
  await expect(items(page).filter({ hasText: 'Possible conflict' })).toHaveCount(0);
  expect(rec.unmatched).toEqual([]);
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

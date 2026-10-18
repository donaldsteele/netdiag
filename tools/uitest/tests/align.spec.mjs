// Alignment guard: icon-only buttons have centred glyphs, icon+text glyphs sit on the
// label line, and no button or page overflows horizontally, in every view, theme and viewport.
import fs from 'node:fs/promises';
import path from 'node:path';
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME } from '../support/mock.mjs';
import { scanToFinish } from '../support/steps.mjs';
import { OUT_DIR, VIEWPORTS } from '../support/config.mjs';
import * as fx from '../fixtures/index.mjs';

async function measure(page, view) {
  return page.evaluate((view) => {
    const out = [];
    const vis = (el) => { const r = el.getBoundingClientRect(); const s = getComputedStyle(el); return r.width > 0 && r.height > 0 && s.visibility !== 'hidden'; };
    const desc = (el) => el.id ? '#' + el.id : el.tagName.toLowerCase() + '.' + [...el.classList].join('.') + ' "' + (el.getAttribute('title') || el.getAttribute('aria-label') || el.textContent.trim().slice(0, 30)) + '"';
    for (const svg of document.querySelectorAll('svg.ico')) {
      if (!vis(svg)) continue;
      const host = svg.parentElement;
      const hr = host.getBoundingClientRect(), ir = svg.getBoundingClientRect();
      // Visible text siblings (not .sr)
      const texts = [...host.childNodes].filter(n => (n.nodeType === 3 && n.textContent.trim()) || (n.nodeType === 1 && n !== svg && !n.classList.contains('sr') && vis(n) && n.textContent.trim()));
      const cs = getComputedStyle(host);
      const padL = parseFloat(cs.paddingLeft) + parseFloat(cs.borderLeftWidth), padR = parseFloat(cs.paddingRight) + parseFloat(cs.borderRightWidth);
      const padT = parseFloat(cs.paddingTop) + parseFloat(cs.borderTopWidth), padB = parseFloat(cs.paddingBottom) + parseFloat(cs.borderBottomWidth);
      const cx = (hr.left + padL + hr.right - padR) / 2, cy = (hr.top + padT + hr.bottom - padB) / 2;
      const icx = (ir.left + ir.right) / 2, icy = (ir.top + ir.bottom) / 2;
      if (texts.length === 0) {
        const dx = icx - cx, dy = icy - cy;
        if (Math.abs(dx) > 1 || Math.abs(dy) > 1) out.push({ view, kind: 'icon-only', el: desc(host), dx: +dx.toFixed(1), dy: +dy.toFixed(1), host: [hr.width, hr.height].map(Math.round), icon: [ir.width, ir.height].map(Math.round) });
      } else {
        // compare icon vertical center with first text line center
        const n = texts[0]; let tr;
        if (n.nodeType === 3) { const rg = document.createRange(); rg.selectNodeContents(n); const rs = [...rg.getClientRects()].filter(r => r.width); tr = rs[0]; } else tr = n.getBoundingClientRect();
        if (!tr) continue;
        // single-line text only
        const fs = parseFloat(getComputedStyle(n.nodeType === 3 ? host : n).fontSize);
        const dy = icy - (tr.top + tr.bottom) / 2;
        if (tr.height < fs * 2 && Math.abs(dy) > 1.5) out.push({ view, kind: 'icon+text', el: desc(host), dy: +dy.toFixed(1), textH: Math.round(tr.height), icon: [ir.width, ir.height].map(Math.round) });
      }
    }
    if (document.documentElement.scrollWidth > innerWidth + 1) out.push({ view, kind: "page-hscroll", el: "html", dy: document.documentElement.scrollWidth - innerWidth });
    for (const b of document.querySelectorAll("button")) { if (!vis(b)) continue; if (b.scrollWidth > b.clientWidth + 1) out.push({ view, kind: "btn-overflow", el: desc(b), dy: b.scrollWidth - b.clientWidth }); }
    return out;
  }, view);
}

for (const [vpName, vp] of Object.entries(VIEWPORTS)) for (const theme of ['dark', 'light']) {
  test(`alignment ${vpName} ${theme}`, async ({ page }) => {
    await initStorage(page, { theme, live: false });
    await installMocks(page, { scenario: 'scan' });
    await page.setViewportSize(vp);
    await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: theme });
    await page.goto(HOME);
    const all = [];
    all.push(...await measure(page, 'empty'));
    await scanToFinish(page);
    await expect(page.locator('#problems-now .pitem button:visible', { hasText: 'Show device' }).first()).toBeVisible();
    all.push(...await measure(page, 'devices'));
    await page.locator('#hosts tbody tr.host button.rowbtn').first().click();
    await expect(page.locator('#drawer')).toBeVisible();
    all.push(...await measure(page, 'drawer-gateway'));
    await page.keyboard.press('Escape');
    await page.locator('#tile-printers').click();
    await page.locator('#hosts tbody tr.host button.rowbtn').first().click();
    await expect(page.locator('#drawer .supplies li').first()).toBeVisible();
    all.push(...await measure(page, 'drawer-printer'));
    await page.keyboard.press('Escape');
    await page.locator('#tile-alive').click();
    await page.locator('#hosts button.rowbtn[data-ip="192.168.2.4"]').click();
    await expect(page.locator('#drawer-remote')).toBeVisible();
    all.push(...await measure(page, 'drawer-remote'));
    await page.keyboard.press('Escape');
    for (const tab of ['health', 'activity', 'history']) {
      const t = page.locator('#tab-' + tab);
      if (await t.isVisible()) { await t.click(); } else { await page.evaluate((tab) => document.querySelector('#tab-' + tab)?.click(), tab); }
      await page.waitForTimeout(200);
      if (tab === 'history') { await page.getByRole('button', { name: 'Compare', exact: true }).click(); await page.waitForTimeout(200); }
      all.push(...await measure(page, tab));
    }
    await page.evaluate(() => document.querySelector('#nav-settings')?.click());
    await expect(page.locator('#settings-dialog')).toBeVisible();
    await page.locator('#mcp-enabled').check().catch(() => page.locator('#mcp-enabled').click());
    await page.waitForTimeout(200);
    all.push(...await measure(page, 'settings'));
    await fs.mkdir(path.join(OUT_DIR, 'probe'), { recursive: true });
    await fs.writeFile(path.join(OUT_DIR, 'probe', `${vpName}-${theme}.json`), JSON.stringify(all, null, 1));
    expect(all).toEqual([]);
  });
}

// The Problems right now card (v0.6 #1): Show device, Open Health, What to
// try (open), Dismiss on fixed items and Show all, then the held and the
// "did not run" lines with their Open Health, at both sizes and themes.
for (const [vpName, vp] of Object.entries(VIEWPORTS)) for (const theme of ['dark', 'light']) {
  test(`alignment problems card ${vpName} ${theme}`, async ({ page }) => {
    await initStorage(page, { theme, live: false });
    const rec = await installMocks(page, { problems: fx.someProblemsView({ fixed: fx.fixedItems() }) });
    await page.setViewportSize(vp);
    await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: theme });
    await page.goto(HOME);
    await scanToFinish(page);
    const card = page.locator('#problems-now');
    await card.locator('#pn-all').click();
    await expect(card.locator('.pitem')).toHaveCount(8);
    await expect(card.locator('.pitem button:visible', { hasText: 'Show device' }).first()).toBeVisible();
    await expect(card.locator('.pitem button:visible', { hasText: 'Open Health' }).first()).toBeVisible();
    await expect(card.locator('.pitem button:visible', { hasText: 'Dismiss' })).toHaveCount(2);
    for (const d of await card.locator('details.ptry').all()) {
      if (await d.isVisible()) await d.locator('summary').click();
    }
    const all = [...await measure(page, 'problems-card')];
    rec.problems = fx.problemsView({ source: 'live', watching: true, held: true, heldReason: 'link', heldSince: new Date().toISOString(),
      open: fx.problemItems() });
    await page.evaluate(() => window.Alpine.$data(document.querySelector('.shell')).loadProblems());
    await expect(card.locator('#pn-held')).toBeVisible();
    all.push(...await measure(page, 'problems-held'));
    rec.problems = fx.problemsView({ source: 'latest', hasHealth: false });
    await page.evaluate(() => window.Alpine.$data(document.querySelector('.shell')).loadProblems());
    await expect(card.locator('#pn-notrun button')).toBeVisible();
    all.push(...await measure(page, 'problems-notrun'));
    expect(all).toEqual([]);
  });
}

// The advice views (v0.6 M7): the banner's What to try, the conflict pair
// with its Technical details, and health cards that all have advice.
for (const [vpName, vp] of Object.entries(VIEWPORTS)) for (const theme of ['dark', 'light']) {
  test(`alignment with advice ${vpName} ${theme}`, async ({ page }) => {
    await initStorage(page, { theme, live: false });
    await installMocks(page, { health: 'all', frames: fx.framesWith(fx.flapHosts(2), [fx.flapConflict(2)], [fx.notice(1)]) });
    await page.setViewportSize(vp);
    await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: theme });
    await page.goto(HOME);
    await scanToFinish(page);
    const all = [];
    await page.locator('#alerts details.btry summary').first().click();
    all.push(...await measure(page, 'banner-advice'));
    await page.keyboard.press('Escape');
    await page.locator(`#hosts button.rowbtn[data-ip="${fx.FLAP_IP}"]`).click();
    await page.locator('#drawer .cbox details.tech summary').click();
    all.push(...await measure(page, 'drawer-conflict'));
    await page.keyboard.press('Escape');
    await page.keyboard.press('Escape');
    await expect(page.locator('#drawer')).toBeHidden();
    await page.evaluate(() => document.querySelector('#tab-health')?.click());
    // The scan's own report has advice on two checks; a recheck gets six.
    await page.getByRole('button', { name: 'Recheck', exact: true }).click();
    await expect(page.locator('.hcard .advice')).toHaveCount(6);
    await page.locator('.hcard details.tech summary').first().click();
    all.push(...await measure(page, 'health-advice'));
    expect(all).toEqual([]);
  });
}

// While live (F9a): Stop live and Restart watching in the top bar, the
// restarted banner with Reload page, and the restart dialog, at every width.
for (const width of [360, 390, 768, 1100, 1440]) {
  test(`alignment live ${width}`, async ({ page }) => {
    await initStorage(page, { theme: 'dark', live: true });
    await installMocks(page, { scenario: 'live', running: true, frames: fx.replayFrames() });
    await page.setViewportSize({ width, height: 800 });
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await page.goto(HOME);
    await expect(page.locator('#restart-btn')).toBeVisible();
    await expect(page.locator('#hosts tbody tr.host')).toHaveCount(fx.TOTAL_HOSTS);
    await page.evaluate(() => { document.querySelector('.shell')._x_dataStack[0].backend = 'restarted'; });
    await expect(page.locator('#backend-restarted')).toBeVisible();
    const all = [...await measure(page, 'live')];
    await page.locator('#restart-btn').click();
    await expect(page.locator('#restart-dialog')).toBeVisible();
    all.push(...await measure(page, 'restart-dialog'));
    expect(all).toEqual([]);
  });
}

// The storm guard holds (v0.6 F9c): the live pill's triangle and its words,
// and the banner for another network with Restart watching, at every width
// in both themes, for checks paused and for alerts stopped.
for (const width of [360, 390, 768, 1100, 1440]) for (const theme of ['dark', 'light']) {
  test(`alignment held ${width} ${theme}`, async ({ page }) => {
    await initStorage(page, { theme, live: true });
    const rec = await installMocks(page, { scenario: 'live', running: true });
    await page.setViewportSize({ width, height: 800 });
    await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: theme });
    const all = [];
    for (const reason of ['link', 'network_changed']) {
      rec.frames = [...fx.replayFrames(), { type: 'live', data: fx.heldStatus(reason) }, { type: 'notice', data: fx.linkNotice(reason) }];
      // The page drops #s= from its address once it has the token, so a
      // second goto(HOME) would only change the hash: load a fresh page.
      await page.goto('about:blank');
      await page.goto(HOME);
      // The held status comes after the whole replayed scan, which takes the
      // page 2 to 6 s to draw when several workers run at once.
      await expect(page.locator('.livepill.held svg.ico')).toBeVisible({ timeout: 20_000 });
      const b = page.locator('#alerts div.banner:not(.more)').filter({ hasText: fx.linkNotice(reason).title });
      await expect(b).toHaveCount(1);
      if (reason === 'network_changed') await expect(b.locator('button.brestart')).toBeVisible();
      all.push(...await measure(page, 'held-' + reason));
    }
    expect(all).toEqual([]);
  });
}

// Team chat (v0.6 M10): the 34 px header tool, the Settings section in each
// state, and the connect wizard's steps, at every width, with nothing
// nested scrolling inside the wizard but the dialog itself.
for (const width of [360, 390, 768, 1100, 1440]) {
  test(`alignment team chat ${width}`, async ({ page }) => {
    await initStorage(page, { theme: 'dark', live: false });
    const rec = await installMocks(page, { chat: 'connected', found: { servers: 2, tray: true },
      channels: [fx.CHAT_CHANNEL, { id: 'C2', name: 'front-desk' }], pairStates: ['approved'] });
    await page.setViewportSize({ width, height: 800 });
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await page.goto(HOME);
    await expect(page.locator('#chat-tool')).toBeVisible();
    const all = [...await measure(page, 'chat-tool')];
    const settings = page.locator('#settings-dialog');
    for (const state of ['connected', 'unreachable', 'needs_reconnect', 'identity_changed', 'off']) {
      rec.chat = fx.bubblesStatus(state);
      await page.evaluate(() => document.querySelector('#nav-settings').click());
      await expect(settings).toHaveJSProperty('open', true);
      await expect(settings.locator('#chat-status .hs')).toHaveText(state === 'off' ? 'Not connected' : /\w/);
      await settings.locator('#set-chat').scrollIntoViewIfNeeded();
      all.push(...await measure(page, 'settings-chat-' + state));
      if (state !== 'off') { await page.keyboard.press('Escape'); await expect(settings).toHaveJSProperty('open', false); }
    }
    const wiz = page.locator('#chat-dialog');
    const nested = () => page.evaluate(() => [...document.querySelectorAll('#chat-dialog *')].filter((el) => {
      const s = getComputedStyle(el);
      return ['auto', 'scroll'].includes(s.overflowY) && el.scrollHeight > el.clientHeight + 1;
    }).map((el) => el.id || el.className));
    await settings.locator('#chat-connect').click();
    await expect(wiz.locator('#wiz-list')).toBeVisible();
    all.push(...await measure(page, 'wizard-list'));
    expect(await nested(), 'wizard list').toEqual([]);
    await wiz.getByRole('button', { name: 'Use this server' }).first().click();
    await expect(wiz.locator('#wiz-fp-start')).toBeVisible();
    all.push(...await measure(page, 'wizard-fingerprint'));
    await wiz.getByRole('button', { name: 'It matches' }).click();
    await expect(wiz.locator('#wiz-code')).toBeVisible();
    all.push(...await measure(page, 'wizard-code'));
    await expect(wiz.getByRole('radio')).toHaveCount(2, { timeout: 10_000 });
    await expect(wiz.locator('#wiz-channel')).toBeVisible();
    all.push(...await measure(page, 'wizard-channel'));
    await wiz.getByRole('radio', { name: '#front-desk' }).check();
    await wiz.getByRole('button', { name: 'Use this channel' }).click();
    await expect(wiz.locator('#wiz-ready')).toBeVisible();
    await wiz.getByRole('button', { name: 'Send test alert' }).click();
    await expect(wiz.locator('#wiz-test-msg')).toContainText('Test alert sent');
    all.push(...await measure(page, 'wizard-ready'));
    expect(await nested(), 'wizard ready').toEqual([]);
    expect(all).toEqual([]);
  });
}

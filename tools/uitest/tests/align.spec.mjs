// Alignment guard: icon-only buttons have centred glyphs, icon+text glyphs sit on the
// label line, and no button or page overflows horizontally, in every view, theme and viewport.
import fs from 'node:fs/promises';
import path from 'node:path';
import { test, expect } from '@playwright/test';
import { installMocks, initStorage } from '../support/mock.mjs';
import { scanToFinish } from '../support/steps.mjs';
import { OUT_DIR, VIEWPORTS } from '../support/config.mjs';

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
    await page.goto('/');
    const all = [];
    all.push(...await measure(page, 'empty'));
    await scanToFinish(page);
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

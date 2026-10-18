// T6: colour-vision deficiency emulation. Status must stay distinguishable by shape,
// not colour alone, and axe must stay clean under each deficiency and theme.
// Output: out/shots/vision-<type>-<theme>-{devices,health,problems}.png and out/axe/vision-<type>-<theme>[-problems].json.
import path from 'node:path';
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME } from '../support/mock.mjs';
import { runAxe } from '../support/axe.mjs';
import { scanToFinish } from '../support/steps.mjs';
import { OUT_DIR, THEMES } from '../support/config.mjs';
import * as fx from '../fixtures/index.mjs';

const DEFICIENCIES = ['deuteranopia', 'protanopia', 'tritanopia'];
const SIZE = { width: 1440, height: 900 };
// Status -> the one shape each state must render (ok/warn/fail from the T6 plan).
const SHAPE = { ok: 'i-circle-check', warn: 'i-triangle-alert', fail: 'i-circle-x' };
const SHAPE_RE = /^icons\.svg#i-[a-z0-9-]+$/;

const hrefOf = (use) => use.replace(/^.*#/, '#');

for (const type of DEFICIENCIES) {
  for (const theme of THEMES) {
    const config = `vision-${type}-${theme}`;
    test(`${type} ${theme}: status shapes, screenshots and axe`, async ({ page }, testInfo) => {
      await initStorage(page, { theme, live: false });
      await installMocks(page, { scenario: 'scan' });
      const cdp = await page.context().newCDPSession(page);
      await cdp.send('Emulation.setEmulatedVisionDeficiency', { type });
      await page.setViewportSize(SIZE);
      await page.goto(HOME);
      await scanToFinish(page);

      // Devices view: device status icons (th svg.sico use) distinguish up / no ping / offline.
      await page.getByRole('button', { name: 'Devices', exact: true }).click();
      const devices = await page.locator('#hosts tbody tr.host th svg.sico').evaluateAll((els) => els.map((el) => ({
        cls: el.getAttribute('class') || '',
        href: el.querySelector('use').getAttribute('href'),
      })));
      expect(devices.length, 'device rows with status icons').toBeGreaterThan(0);
      for (const { href } of devices) expect(href).toMatch(SHAPE_RE);
      const deviceHrefs = devices.map((d) => d.href);
      expect(deviceHrefs).toContain('icons.svg#i-circle-check');
      expect(deviceHrefs).toContain('icons.svg#i-circle-help');
      // Offline (s-bad) rows use the fail shape, and only those.
      for (const { cls, href } of devices) {
        expect(cls.includes('s-bad'), `s-bad <=> circle-x (${href})`).toBe(href === 'icons.svg#i-circle-x');
      }
      await page.screenshot({ path: path.join(OUT_DIR, 'shots', `${config}-devices.png`) });

      // Health view: pills (header strip) and health cards must map status -> shape.
      await page.getByRole('button', { name: 'Health', exact: true }).click();
      const marks = await page.locator('.hpill, .hcard .hs').evaluateAll((els) => els.map((el) => {
        const cls = [...el.classList].find((c) => /^hs-/.test(c)) || '';
        const uses = el.querySelectorAll('svg use');
        return { status: cls.replace(/^hs-/, ''), shape: uses[uses.length - 1].getAttribute('href') };
      }));
      expect(marks.length, 'health pills and cards').toBeGreaterThan(0);
      const statuses = new Set();
      const shapes = new Set();
      for (const { status, shape } of marks) {
        expect(shape, `shape for status ${status}`).toMatch(SHAPE_RE);
        statuses.add(status);
        shapes.add(hrefOf(shape));
        if (SHAPE[status]) expect(shape, `${status} shape`).toBe('icons.svg#' + SHAPE[status]);
      }
      // Each distinct state gets its own shape (circle-help covers unavailable/unknown).
      expect(shapes.size, 'distinct shapes per distinct status').toBe(statuses.size);
      await page.screenshot({ path: path.join(OUT_DIR, 'shots', `${config}-health.png`) });

      await runAxe(page, config, testInfo);
    });
  }
}

// The Problems right now card (v0.6 #1): each severity has its own shape
// next to its word (Problem, Warning, Note, Fixed), and the shapes stay
// apart under each deficiency, in both themes.
const CARD_SHAPE = { Problem: 'icons.svg#i-circle-x', Warning: 'icons.svg#i-triangle-alert', Note: 'icons.svg#i-info', Fixed: 'icons.svg#i-circle-check' };
for (const type of DEFICIENCIES) {
  for (const theme of THEMES) {
    const config = `vision-${type}-${theme}-problems`;
    test(`${type} ${theme}: the Problems right now card's severities have distinct shapes`, async ({ page }, testInfo) => {
      await initStorage(page, { theme, live: false });
      await installMocks(page, { problems: fx.someProblemsView({ fixed: fx.fixedItems() }) });
      const cdp = await page.context().newCDPSession(page);
      await cdp.send('Emulation.setEmulatedVisionDeficiency', { type });
      await page.setViewportSize(SIZE);
      await page.goto(HOME);
      await page.locator('#pn-all').click();
      await expect(page.locator('#problems-now .pitem')).toHaveCount(8);
      const marks = await page.locator('#problems-now .pitem .psev').evaluateAll((els) => els.map((el) => ({
        word: el.textContent.trim(), shape: el.querySelector('use').getAttribute('href'),
      })));
      const byWord = {};
      for (const { word, shape } of marks) {
        expect(CARD_SHAPE[word], `a card word: ${word}`).toBeTruthy();
        expect(shape, `${word} shape`).toBe(CARD_SHAPE[word]);
        byWord[word] = shape;
      }
      expect(Object.keys(byWord).sort()).toEqual(Object.keys(CARD_SHAPE).sort());
      expect(new Set(Object.values(byWord)).size, 'one shape per severity').toBe(4);
      await page.locator('#problems-now').screenshot({ path: path.join(OUT_DIR, 'shots', `${config}.png`) });
      await runAxe(page, config, testInfo);
    });
  }
}

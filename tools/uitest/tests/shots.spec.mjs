// Screenshots for {dark, light} x {desktop 1440x900, narrow 390x844} plus an axe scan of each.
// Output: out/shots/<theme>-<width>x<height>.png and out/axe/<config>.json.
import path from 'node:path';
import { test } from '@playwright/test';
import { installMocks, initStorage } from '../support/mock.mjs';
import { runAxe } from '../support/axe.mjs';
import { scanToFinish } from '../support/steps.mjs';
import { OUT_DIR, THEMES, VIEWPORTS } from '../support/config.mjs';

for (const theme of THEMES) {
  for (const [name, size] of Object.entries(VIEWPORTS)) {
    const config = `${theme}-${size.width}x${size.height}`;
    test(`screenshot and axe: ${theme} ${name} (${size.width}x${size.height})`, async ({ page }, testInfo) => {
      await initStorage(page, { theme, live: false });
      await installMocks(page, { scenario: 'scan' });
      await page.setViewportSize(size);
      await page.goto('/');
      await scanToFinish(page);
      await page.screenshot({
        path: path.join(OUT_DIR, 'shots', `${config}.png`),
        fullPage: true,
      });
      await runAxe(page, config, testInfo);
    });
  }
}

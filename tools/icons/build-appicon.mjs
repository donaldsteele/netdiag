// Dev-only: renders ../../docs/assets/favicon.svg to the PNG sizes embedded as
// the Windows exe icon (../../cmd/netdiag/winres/icon-<size>.png).
// Uses the Playwright Chromium installed by tools/uitest.
// Usage: (cd ../uitest && npm ci && npx playwright install chromium) && node build-appicon.mjs
import { readFileSync, mkdirSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const SVG = join(here, '..', '..', 'docs', 'assets', 'favicon.svg');
const OUT = join(here, '..', '..', 'cmd', 'netdiag', 'winres');
const SIZES = [16, 24, 32, 48, 64, 256];

const { chromium } = createRequire(join(here, '..', 'uitest', 'package.json'))('playwright');
const svg = readFileSync(SVG, 'utf8');
mkdirSync(OUT, { recursive: true });

const browser = await chromium.launch();
try {
  const page = await browser.newPage();
  for (const size of SIZES) {
    await page.setViewportSize({ width: size, height: size });
    await page.setContent(
      `<style>html,body{margin:0;background:transparent}svg{display:block;width:${size}px;height:${size}px}</style>${svg}`,
    );
    const path = join(OUT, `icon-${size}.png`);
    await page.locator('svg').screenshot({ path, omitBackground: true });
    console.log('wrote', path);
  }
} finally {
  await browser.close();
}

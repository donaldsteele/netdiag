// Dev-only Playwright config for the netdiag UI harness. See README.md.
import { defineConfig } from '@playwright/test';
import { BASE_URL, OUT_DIR } from './support/config.mjs';

export default defineConfig({
  testDir: './tests',
  outputDir: OUT_DIR + '/test-results',
  timeout: 60_000,
  fullyParallel: true,
  reporter: [['list']],
  use: {
    baseURL: BASE_URL,
    browserName: 'chromium',
    headless: true,
    viewport: { width: 1440, height: 900 },
    colorScheme: 'dark',
    reducedMotion: 'no-preference',
    serviceWorkers: 'block',
  },
  projects: [{ name: 'chromium' }],
});

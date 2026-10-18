// The Problems filter uses the supply threshold from Settings, like the
// "Supply low" alert does, instead of a fixed 15%.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME } from '../support/mock.mjs';
import * as fx from '../fixtures/index.mjs';

// One printer with no alerts, no conflict, a working web page and a toner
// level of pct (state ok), so only the threshold decides whether it is a problem.
function printerFrames(pct) {
  const base = fx.hosts()[9]; // 192.168.2.10
  const h = { ...base, conflicts: [], printer: { ...base.printer, alerts: [], supplies: [
    { description: 'Black Toner', type: 'toner', color: 'black', level: pct * 10, max: 1000, percent: pct, unit: 'impressions', state: 'ok' },
  ] } };
  return [
    { type: 'host', data: h },
    { type: 'done', data: { ...fx.scanResult({ list: [h] }), conflicts: [] } },
  ];
}

function settingsWith(pct) {
  const s = fx.settings();
  const alerts = { ...s.alerts };
  if (pct === undefined) delete alerts.supplyThresholdPct; else alerts.supplyThresholdPct = pct;
  return { ...s, alerts };
}

async function scanOne(page) {
  await page.getByRole('button', { name: 'Scan', exact: true }).click();
  await expect(page.locator('section.progress .stage')).toHaveText('Finished');
  await expect(page.locator('#hosts tbody tr.host')).toHaveCount(1);
}

const problemsTab = (page) => page.locator('.tabs button.tab', { hasText: /^Problems \(/ });

test('Problems follows the supply threshold in Settings', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { settings: settingsWith(50), frames: printerFrames(40) });
  await page.goto(HOME);
  await scanOne(page);
  // 40% is at or below the 50% threshold.
  await expect(problemsTab(page)).toHaveText('Problems (1)');
  await problemsTab(page).click();
  await expect(page.locator('#hosts tbody tr.host')).toHaveCount(1);

  // Lower the threshold to 30%: 40% is no longer a problem.
  await page.locator('#nav-settings').click();
  const dialog = page.locator('#settings-dialog');
  await expect(dialog).toBeVisible();
  await dialog.getByLabel('Supply threshold (%, 1-100)').fill('30');
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(dialog).toBeHidden();
  await expect(problemsTab(page)).toHaveText('Problems (0)');
  await expect(page.locator('#hosts tbody tr.host')).toHaveCount(0);
  expect(rec.unmatched).toEqual([]);
});

test('Problems uses 15% when settings have no supply threshold', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { settings: settingsWith(undefined), frames: printerFrames(15) });
  await page.goto(HOME);
  await scanOne(page);
  await expect(problemsTab(page)).toHaveText('Problems (1)');
});

test('Problems uses 15% before settings load', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  await installMocks(page, { frames: printerFrames(20) });
  // The data store is down: no settings at all.
  await page.route('**/api/settings', (route) => route.fulfill({ status: 503, contentType: 'application/json', body: '{"error":"data store unavailable"}' }));
  await page.goto(HOME);
  await scanOne(page);
  await expect(problemsTab(page)).toHaveText('Problems (0)');
});

// "Ignore this MAC" asks first, says how to undo it, and Settings can undo it.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage, HOME } from '../support/mock.mjs';
import { scanToFinish } from '../support/steps.mjs';

const MAC = '00:1B:A9:AA:00:30';

test('Ignore this MAC asks for confirmation and can be undone in Settings', async ({ page }) => {
  await initStorage(page, { theme: 'dark', live: false });
  const rec = await installMocks(page, { scenario: 'scan' });
  await page.goto(HOME);
  await scanToFinish(page);
  const posts = () => rec.requests.filter((r) => r.method === 'POST' && r.path === '/api/conflicts/ignore');

  await page.locator('#hosts button.rowbtn[data-ip="192.168.2.30"]').click();
  const drawer = page.locator('#drawer');
  const conflicts = drawer.getByRole('heading', { name: 'Conflicts', exact: true });
  const ignore = drawer.getByRole('button', { name: 'Ignore this MAC ' + MAC });
  await expect(conflicts).toBeVisible();

  // Cancel: nothing is sent and the conflict stays.
  let message = '';
  page.once('dialog', (d) => { message = d.message(); d.dismiss(); });
  await ignore.click();
  await expect.poll(() => message).toContain(MAC);
  expect(message).toContain('stop warning about conflicts');
  expect(message).toContain('open Settings');
  expect(message).toContain('Ignored conflict MACs');
  expect(posts()).toEqual([]);
  await expect(conflicts).toBeVisible();

  // OK: the MAC is ignored (JSON POST) and the conflict goes away.
  page.once('dialog', (d) => d.accept());
  await ignore.click();
  await expect.poll(() => posts().length).toBe(1);
  expect(posts()[0].contentType).toContain('application/json');
  expect(JSON.parse(posts()[0].body)).toEqual({ mac: MAC });
  await expect(conflicts).toBeHidden();

  // Undo, as the confirmation says: Settings lists the MAC with Remove.
  await page.keyboard.press('Escape');
  await expect(drawer).toBeHidden();
  await page.locator('#nav-settings').click();
  const dialog = page.locator('#settings-dialog');
  await expect(dialog).toBeVisible();
  const remove = dialog.getByRole('button', { name: 'Remove ' + MAC });
  await expect(remove).toBeVisible();
  await remove.click();
  await expect(remove).toHaveCount(0);
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(dialog).toBeHidden();
  const put = rec.requests.filter((r) => r.method === 'PUT' && r.path === '/api/settings').pop();
  const doc = JSON.parse(put.body);
  expect(doc.ignoredConflictMACs).toEqual([]);
  // The rest of the settings document is sent back unchanged.
  expect(doc.community).toBe('public');
  expect(doc.alerts.supplyThresholdPct).toBe(15);
  expect(doc.alerts.triggers.ipConflict).toBe(true);
  expect(rec.unmatched).toEqual([]);
  expect(rec.external).toEqual([]);
});

// Reusable UI steps shared by the specs.
import { expect } from '@playwright/test';
import { TOTAL_HOSTS } from '../fixtures/index.mjs';

// Press Scan on a "done" scenario and wait until every fixture host is rendered.
export async function scanToFinish(page) {
  await page.getByRole('button', { name: 'Scan', exact: true }).click();
  await expect(page.locator('section.progress .stage')).toHaveText('Finished');
  await expect(page.locator('#hosts tbody tr.host')).toHaveCount(TOTAL_HOSTS);
}

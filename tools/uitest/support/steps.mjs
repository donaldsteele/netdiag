// Reusable UI steps shared by the specs.
import { expect } from '@playwright/test';
import { TOTAL_HOSTS } from '../fixtures/index.mjs';

// Press Scan on a "done" scenario and wait until every fixture host is rendered.
export async function scanToFinish(page) {
  await page.getByRole('button', { name: 'Scan', exact: true }).click();
  await expect(page.locator('section.progress .stage')).toHaveText('Finished');
  await expect(page.locator('#hosts tbody tr.host')).toHaveCount(TOTAL_HOSTS);
}

// A time as the page's pnClock writes it (local time): "10:42" today,
// "Fri 10:42" earlier this week, "10 Oct 10:42" before.
export function cardClock(at) {
  const d = new Date(at), now = new Date();
  const t = d.toTimeString().slice(0, 5);
  const day = (x) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const ago = Math.round((day(now) - day(d)) / 86400000);
  if (ago === 0) return t;
  if (ago > 0 && ago < 7) return ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'][d.getDay()] + ' ' + t;
  return d.getDate() + ' ' + ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'][d.getMonth()] + ' ' + t;
}

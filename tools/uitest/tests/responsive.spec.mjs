// T8: responsive pass. No horizontal page scroll at 360/390/768/1100/1440 in every view,
// the drawer, and the settings dialog. Wide tables scroll inside .tablewrap, not the page.
import { test, expect } from '@playwright/test';
import { installMocks, initStorage } from '../support/mock.mjs';
import { scanToFinish } from '../support/steps.mjs';

const WIDTHS = [360, 390, 768, 1100, 1440];
const HEIGHT = 800;

// Overflow of the document in CSS pixels. Anything above 0 is a horizontal page scroll.
const pageOverflow = (page) => page.evaluate(() => {
  const el = document.documentElement;
  return el.scrollWidth - el.clientWidth;
});

// Narrow layouts hide the sidebar behind the menu button; open it first.
async function navTo(page, tabSel) {
  const menu = page.locator('.menubtn');
  if (await menu.isVisible() && (await menu.getAttribute('aria-expanded')) !== 'true') {
    await menu.click();
  }
  await page.locator(tabSel).click();
}

for (const width of WIDTHS) {
  test(`no horizontal page scroll at ${width}px in every view`, async ({ page }) => {
    await page.setViewportSize({ width, height: HEIGHT });
    await initStorage(page, { theme: 'dark', live: false });
    await installMocks(page, { scenario: 'scan' });
    await page.goto('/');
    await scanToFinish(page);
    expect(await pageOverflow(page), 'devices view').toBeLessThanOrEqual(0);

    await page.locator('#hosts button.rowbtn[data-ip="192.168.2.10"]').click();
    await expect(page.locator('#drawer')).toBeVisible();
    expect(await pageOverflow(page), 'drawer open').toBeLessThanOrEqual(0);
    await page.getByRole('button', { name: 'Close details' }).click();
    await expect(page.locator('#drawer')).toBeHidden();

    for (const tab of ['#tab-health', '#tab-activity', '#tab-history']) {
      await navTo(page, tab);
      await expect(page.locator(tab)).toHaveAttribute('aria-current', 'page');
      if (tab === '#tab-history') await expect(page.locator('#history tbody tr').first()).toBeVisible();
      expect(await pageOverflow(page), 'view ' + tab).toBeLessThanOrEqual(0);
    }

    // The diff tables only exist after a compare; they must scroll inside their own container too.
    await navTo(page, '#tab-history');
    await page.locator('#history').getByRole('button', { name: 'Compare', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Added (1)' })).toBeVisible();
    expect(await pageOverflow(page), 'history diff').toBeLessThanOrEqual(0);
  });
}

// At narrow widths the settings dialog must keep its Save action reachable.
for (const width of [360, 390]) {
  test(`settings Save stays reachable at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: HEIGHT });
    await initStorage(page, { theme: 'dark', live: false });
    await installMocks(page, { scenario: 'scan' });
    await page.goto('/');
    await navTo(page, '#nav-settings');
    const dialog = page.locator('#settings-dialog');
    await expect(dialog).toBeVisible();
    const save = dialog.getByRole('button', { name: 'Save', exact: true });
    await save.scrollIntoViewIfNeeded();
    await expect(save).toBeInViewport();
    expect(await pageOverflow(page), 'settings open').toBeLessThanOrEqual(0);
  });
}

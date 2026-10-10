// Shared harness settings. Dev-only: nothing here is embedded in netdiag.
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
export const UITEST_DIR = path.resolve(HERE, '..');
// Static UI assets served to the browser by the route mock (repo web/ tree).
export const WEB_DIR = path.resolve(UITEST_DIR, '..', '..', 'web');
// Screenshots, axe reports and Playwright output. Git-ignored.
export const OUT_DIR = path.join(UITEST_DIR, 'out');

// Synthetic origin. Requests are intercepted by page.route, so nothing listens here.
export const BASE_URL = 'http://127.0.0.1:8765';

// THE axe gate switch. false: serious/critical axe violations are attached and
// logged but do not fail (T2 baseline against the old layout). true: they fail
// the test. Flip to true when the layout milestones make the UI axe-clean.
export const AXE_ENFORCE = true;

export const THEMES = ['dark', 'light'];
export const VIEWPORTS = {
  desktop: { width: 1440, height: 900 },
  narrow: { width: 390, height: 844 },
};

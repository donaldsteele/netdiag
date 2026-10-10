// axe-core runner shared by the screenshot specs. Gate behaviour is controlled
// by AXE_ENFORCE in config.mjs.
import fs from 'node:fs/promises';
import path from 'node:path';
import AxeBuilder from '@axe-core/playwright';
import { expect } from '@playwright/test';
import { AXE_ENFORCE, OUT_DIR } from './config.mjs';

const TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'];

export async function runAxe(page, name, testInfo) {
  const results = await new AxeBuilder({ page }).withTags(TAGS).analyze();
  const summary = results.violations.map((v) => ({
    id: v.id, impact: v.impact, help: v.help, nodes: v.nodes.length,
    targets: v.nodes.slice(0, 5).map((n) => n.target.join(' ')),
  }));
  const blocking = summary.filter((v) => v.impact === 'serious' || v.impact === 'critical');

  const file = path.join(OUT_DIR, 'axe', `${name}.json`);
  await fs.mkdir(path.dirname(file), { recursive: true });
  await fs.writeFile(file, JSON.stringify(summary, null, 2));
  await testInfo.attach(`axe-${name}`, { path: file, contentType: 'application/json' });

  const mode = AXE_ENFORCE ? 'enforced' : 'reported only (AXE_ENFORCE=false)';
  console.log(`[axe:${name}] ${summary.length} violation(s), ${blocking.length} serious/critical, ${mode}`);
  if (AXE_ENFORCE) {
    expect(blocking, `serious/critical axe violations in ${name}`).toEqual([]);
  }
  return summary;
}

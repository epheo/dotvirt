// Proof that the New menu's platform-authoring actions are gated per-capability:
// run once with an admin token and once with a restricted (namespace-only) token
// and compare. OC_TOKEN + LABEL env. → /tmp/perms-<label>.png
import { chromium } from 'playwright';

const TOKEN = process.env.OC_TOKEN;
const LABEL = process.env.LABEL || 'x';
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
await page.goto('http://localhost:5173');
await page.waitForSelector('textarea');
await page.fill('textarea', TOKEN);
await page.click('button[type="submit"]');
await page.getByRole('button', { name: 'Topology' }).waitFor({ timeout: 25000 });
await page.waitForTimeout(700);
await page.locator('header').getByRole('button', { name: 'New', exact: true }).click();
await page.waitForTimeout(400);
await page.screenshot({ path: `/tmp/perms-${LABEL}.png` });
console.log('ok', LABEL);
await browser.close();

// Verify the DEPLOYED app (through the cluster route) carries the new UI:
// login, dark theme, wait for a real VM row, screenshot.
import { execSync } from 'node:child_process';
import { chromium } from 'playwright';

const base = process.env.BASE || 'https://dotvirt.apps.cluster-csqjp.dyn.redhatworkshops.io';
const out = process.env.OUT || '/tmp';
const token = execSync('oc whoami -t', { encoding: 'utf8' }).trim();

const browser = await chromium.launch();
const ctx = await browser.newContext({
	viewport: { width: 1440, height: 900 },
	ignoreHTTPSErrors: true,
});
await ctx.addInitScript(`localStorage.setItem('dotvirt.theme', JSON.stringify('dark'))`);
const page = await ctx.newPage();
await page.goto(base, { waitUntil: 'networkidle' });
await page.locator('textarea').fill(token);
await page.getByRole('button', { name: 'Sign in' }).click();
await page.locator('aside').getByText('burn-1').waitFor({ timeout: 60000 });
await page.goto(`${base}/compute?tab=vms`, { waitUntil: 'networkidle' });
await page.locator('tbody tr').first().waitFor({ timeout: 15000 });
console.log('theme:', await page.evaluate(() => document.documentElement.dataset.theme));
await page.screenshot({ path: `${out}/deployed-dark.png` });
console.log('deployed-dark ok');
await browser.close();

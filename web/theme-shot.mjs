// Smoke-shot the login screen in both themes against `vite preview` (no
// backend: /api/me fails → Login renders). Verifies the dark override block,
// the FOUC stamp, and basic token readability.
import { chromium } from 'playwright';

const base = process.env.BASE || 'http://localhost:4173';
const out = process.env.OUT || '/tmp';

const browser = await chromium.launch();
for (const mode of ['light', 'dark']) {
	const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
	await ctx.addInitScript(`localStorage.setItem('dotvirt.theme', JSON.stringify('${mode}'))`);
	const page = await ctx.newPage();
	await page.goto(base, { waitUntil: 'networkidle' });
	await page.waitForTimeout(500);
	const stamped = await page.evaluate(() => document.documentElement.dataset.theme);
	const bodyBg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
	console.log(`${mode}: data-theme=${stamped} body-bg=${bodyBg}`);
	await page.screenshot({ path: `${out}/login-${mode}.png` });
	await ctx.close();
}
await browser.close();

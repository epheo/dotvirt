// Perf-tab theme verification with a real paint gate: wait until the first
// chart canvas has a meaningful number of painted pixels, then shoot — in
// light, after a live flip to dark, and in a fresh dark context.
import { execSync } from 'node:child_process';
import { chromium } from 'playwright';

const base = 'http://localhost:5173';
const out = process.env.OUT || '/tmp';
const token = execSync('oc whoami -t', { encoding: 'utf8' }).trim();
const browser = await chromium.launch();

async function open(mode) {
	const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
	await ctx.addInitScript(`localStorage.setItem('dotvirt.theme', JSON.stringify('${mode}'))`);
	const page = await ctx.newPage();
	await page.goto(base, { waitUntil: 'networkidle' });
	await page.locator('textarea').fill(token);
	await page.getByRole('button', { name: 'Sign in' }).click();
	await page.locator('aside').getByText('burn-1').waitFor({ timeout: 60000 });
	return { ctx, page };
}

async function openPerf(page) {
	await page.goto(`${base}/vm/drs-lab/burn-1?tab=monitor`, { waitUntil: 'networkidle' });
	await page.locator('main').getByText('Performance').first().click();
	await page.locator('.uplot-host canvas').first().waitFor({ timeout: 30000 });
}

async function waitPainted(page) {
	await page.waitForFunction(
		() => {
			const c = document.querySelector('.uplot-host canvas');
			if (!c) return false;
			const d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data;
			let n = 0;
			for (let i = 3; i < d.length; i += 4) if (d[i] !== 0) n++;
			return n > 2000;
		},
		{ timeout: 30000 },
	);
	await page.waitForTimeout(300);
}

const light = await open('light');
await openPerf(light.page);
await waitPainted(light.page);
await light.page.screenshot({ path: `${out}/perf-light.png` });
console.log('perf-light ok');

// Live flip on the loaded charts: the rebuild must repaint with dark colors.
await light.page.locator('header').getByRole('button', { name: /admin/ }).click();
await light.page.getByRole('button', { name: 'Dark' }).click();
await light.page.keyboard.press('Escape');
await waitPainted(light.page);
const mode = await light.page.evaluate(() => document.documentElement.dataset.theme);
if (mode !== 'dark') throw new Error(`expected dark, got ${mode}`);
await light.page.screenshot({ path: `${out}/perf-flipped-dark.png` });
console.log('perf-flipped-dark ok');
await light.ctx.close();

const dark = await open('dark');
await openPerf(dark.page);
await waitPainted(dark.page);
await dark.page.screenshot({ path: `${out}/perf-dark.png` });
console.log('perf-dark ok');
await browser.close();

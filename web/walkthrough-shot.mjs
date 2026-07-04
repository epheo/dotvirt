// Closing verification pass for the UX polish program: drive the new SPA
// (vite dev :5173 → local backend :8080 → workshop cluster) through every
// section in light, flip the theme live via the user menu, and sweep again in
// dark. The login token is fetched in-process (never persisted).
import { execSync } from 'node:child_process';
import { chromium } from 'playwright';

const base = 'http://localhost:5173';
const out = process.env.OUT || '/tmp';
const token = execSync('oc whoami -t', { encoding: 'utf8' }).trim();

const browser = await chromium.launch();
const failures = [];
let page;

// One context per theme: addInitScript re-runs on every navigation, so a
// live flip inside a context gets re-stamped — the flip test covers that
// path; the sweeps pin the theme per context.
async function open(mode) {
	const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
	await ctx.addInitScript(`localStorage.setItem('dotvirt.theme', JSON.stringify('${mode}'))`);
	page = await ctx.newPage();
	await page.goto(base, { waitUntil: 'networkidle' });
	await page.locator('textarea').fill(token);
	await page.getByRole('button', { name: 'Sign in' }).click();
	await page.locator('aside').getByText('burn-1').waitFor({ timeout: 60000 });
	return ctx;
}

async function shot(name) {
	const mode = await page.evaluate(() => document.documentElement.dataset.theme);
	await page.screenshot({ path: `${out}/wt-${name}-${mode}.png` });
	console.log(`shot wt-${name}-${mode}`);
}

async function step(name, fn) {
	try {
		await fn();
	} catch (e) {
		failures.push(`${name}: ${String(e).split('\n')[0]}`);
		console.log(`FAIL ${name}: ${String(e).split('\n')[0]}`);
	}
}

async function sweep() {
	await step('compute-grid', async () => {
		await page.goto(`${base}/compute?tab=vms`, { waitUntil: 'networkidle' });
		await page.locator('tbody tr').first().waitFor({ timeout: 15000 });
		await shot('compute-vms');
	});
	await step('compute-summary', async () => {
		await page.goto(`${base}/compute?tab=summary`, { waitUntil: 'networkidle' });
		await page.waitForTimeout(2500); // rings/quotas fetch
		await shot('compute-summary');
	});
	await step('vm-summary', async () => {
		await page.goto(`${base}/vm/drs-lab/burn-1?tab=summary`, { waitUntil: 'networkidle' });
		await page.getByText('Capacity', { exact: false }).first().waitFor({ timeout: 15000 });
		await page.waitForTimeout(2000);
		await shot('vm-summary');
	});
	await step('vm-monitor-perf', async () => {
		await page.goto(`${base}/vm/drs-lab/burn-1?tab=monitor`, { waitUntil: 'networkidle' });
		await page.locator('main').getByText('Performance').first().click();
		await page.locator('.uplot-host canvas').first().waitFor({ timeout: 20000 });
		await page.waitForTimeout(1500);
		await shot('vm-perf');
	});
	await step('vm-configure', async () => {
		await page.goto(`${base}/vm/drs-lab/burn-1?tab=configure`, { waitUntil: 'networkidle' });
		await page.waitForTimeout(1000);
		await shot('vm-configure');
	});
	await step('hosts', async () => {
		await page.goto(`${base}/hosts`, { waitUntil: 'networkidle' });
		await page.waitForTimeout(2000);
		await shot('hosts');
	});
	await step('networking', async () => {
		await page.goto(`${base}/networking`, { waitUntil: 'networkidle' });
		await page.waitForTimeout(1500);
		await shot('networking');
	});
	await step('storage', async () => {
		await page.goto(`${base}/storage`, { waitUntil: 'networkidle' });
		await page.waitForTimeout(1500);
		await shot('storage');
	});
	await step('catalog', async () => {
		await page.goto(`${base}/catalog`, { waitUntil: 'networkidle' });
		await page.waitForTimeout(1500);
		await shot('catalog');
	});
	await step('dock-alarms', async () => {
		await page.goto(`${base}/compute?tab=vms`, { waitUntil: 'networkidle' });
		await page.locator('tbody tr').first().waitFor({ timeout: 15000 });
		await page.getByRole('button', { name: 'Alarms', exact: false }).click();
		await page.waitForTimeout(1000);
		await shot('dock-alarms');
	});
	await step('clone-modal', async () => {
		await page.goto(`${base}/vm/drs-lab/burn-1?tab=summary`, { waitUntil: 'networkidle' });
		await page.getByRole('button', { name: 'Actions' }).click();
		await page.getByRole('button', { name: /Clone…/ }).click();
		await page.locator('[role="dialog"]').waitFor({ timeout: 5000 });
		await shot('clone-modal');
		await page.keyboard.press('Escape');
	});
}

const lightCtx = await open('light');
await sweep();

// Flip to dark LIVE through the user menu (exercises store + synchronous
// stamp + uPlot rebuild), starting from the perf charts so the flip is
// visible on canvas.
await step('theme-flip', async () => {
	await page.goto(`${base}/vm/drs-lab/burn-1?tab=monitor`, { waitUntil: 'networkidle' });
	await page.locator('main').getByText('Performance').first().click();
	await page.locator('.uplot-host canvas').first().waitFor({ timeout: 20000 });
	await page.locator('header').getByRole('button', { name: /admin/ }).click();
	await page.getByRole('button', { name: 'Dark' }).click();
	await page.keyboard.press('Escape');
	await page.waitForTimeout(1200);
	const mode = await page.evaluate(() => document.documentElement.dataset.theme);
	if (mode !== 'dark') throw new Error(`data-theme=${mode} after toggle`);
	await shot('vm-perf-flipped');
});
await lightCtx.close();

await open('dark');
await sweep();

console.log(failures.length ? `FAILURES:\n${failures.join('\n')}` : 'ALL STEPS OK');
await browser.close();
process.exit(failures.length ? 1 : 0);

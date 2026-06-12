// Phase 3 verification drive: container Performance sub-rail (3.2) and the
// Clone → NotTracked → Adopt-into-git flow (3.1), with screenshots to /tmp.
// Run inside the dev container: OC_TOKEN=$(oc whoami -t) node phase3-shot.mjs
import { chromium } from 'playwright';

const TOKEN = process.env.OC_TOKEN;
const BASE = 'http://localhost:5173';
const OUT = '/tmp';
const SRC = 'vm-tenant-a';
const TARGET = process.env.CLONE_TARGET || 'vm-tenant-a-ui';

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
page.setDefaultTimeout(30000);

await page.goto(BASE);
await page.waitForSelector('textarea');
await page.fill('textarea', TOKEN);
await page.click('button[type="submit"]');
// Gate on streamed inventory, not a fixed sleep (see icon-shot).
await page.waitForSelector(`text=${SRC}`, { timeout: 20000 });

// --- 3.2: container Monitor → Performance sub-rail ---
await page.getByRole('button', { name: 'Monitor', exact: true }).click();
await page.getByRole('button', { name: 'performance' }).click();
await page.waitForSelector('.uplot', { timeout: 30000 });
await page.waitForTimeout(1000); // legends settle
await page.screenshot({ path: `${OUT}/phase3-1-scope-performance.png` });
console.log('scope performance: charts rendered');

// --- 3.1: open the source VM and clone it ---
await page.getByRole('button', { name: 'VMs', exact: true }).click();
await page.locator('tbody tr', { hasText: SRC }).first().click();
await page.waitForSelector('text=All VMs');
await page.getByRole('button', { name: /^Actions/ }).click();
await page.getByRole('button', { name: 'Clone…' }).click();
await page.waitForSelector('#clone-target-input');
await page.fill('#clone-target-input', TARGET);
await page.screenshot({ path: `${OUT}/phase3-2-clone-modal.png` });
await page.getByRole('button', { name: 'Clone', exact: true }).click();
// The modal polls the clone list; wait for the phase to settle.
await page.waitForSelector('text=Succeeded', { timeout: 180000 });
await page.screenshot({ path: `${OUT}/phase3-3-clone-succeeded.png` });
console.log('clone: succeeded');
// The footer Close (the header X also has the accessible name "Close").
await page.locator('footer').getByRole('button', { name: 'Close' }).click();

// --- the target lands NotTracked; open it ---
await page.getByRole('button', { name: 'All VMs' }).first().click();
await page.getByRole('button', { name: 'VMs', exact: true }).click();
// The new VM arrives over the live stream.
await page.waitForSelector(`tbody tr:has-text("${TARGET}")`, { timeout: 60000 });
await page.locator('tbody tr', { hasText: TARGET }).first().click();
await page.waitForSelector('text=Not in git', { timeout: 15000 });
await page.screenshot({ path: `${OUT}/phase3-4-nottracked-banner.png` });
console.log('target: NotTracked banner shown');

// --- adopt into git (retry while the running-branch export catches up) ---
for (let attempt = 1; ; attempt++) {
	await page.getByRole('button', { name: 'Adopt into git' }).click();
	try {
		await page.waitForSelector('text=staged into Changes', { timeout: 8000 });
		break;
	} catch {
		if (attempt >= 8) throw new Error('adopt never succeeded');
		await page.waitForTimeout(12000); // export tick (30s) + git poll (10s)
	}
}
await page.screenshot({ path: `${OUT}/phase3-5-adopted.png` });
console.log('adopt: staged into Changes');

// The Changes badge should now show the staged create.
await page.getByRole('button', { name: /^Changes/ }).click();
await page.waitForSelector(`text=${TARGET}`);
await page.waitForTimeout(500);
await page.screenshot({ path: `${OUT}/phase3-6-changes-panel.png` });
console.log('changes panel: create item present');

await browser.close();
console.log('OK');

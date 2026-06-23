// Screenshot driver for the NSX-T networking revisit: the topology map, the split
// "New Segment" modal, the Tier-1 egress-firewall modal, and the dual-vocab segment
// Configure panel. Gates on the new pinned Topology entry (proves the tree rendered)
// rather than a VM row, since the landing tab is Summary.
// Run: OC_TOKEN=$(oc whoami -t) node net-shot.mjs  → /tmp/net-*.png
import { chromium } from 'playwright';

const TOKEN = process.env.OC_TOKEN;
const BASE = process.env.BASE_URL ?? 'http://localhost:5173';
const OUT = '/tmp';

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
const done = [];
async function snap(name) {
	await page.screenshot({ path: `${OUT}/net-${name}.png` });
	done.push(name);
}
async function step(name, fn) {
	try {
		await fn();
		await page.waitForTimeout(600);
		await snap(name);
	} catch (e) {
		console.log(`STEP ${name} FAILED: ${e.message}`);
	}
}
async function closeModal() {
	try {
		await page.getByRole('button', { name: 'Cancel' }).click({ timeout: 2000 });
	} catch {}
	try {
		await page.locator('div[role="presentation"]').waitFor({ state: 'detached', timeout: 3000 });
	} catch {}
}

await page.goto(BASE);
await page.waitForSelector('textarea');
await page.fill('textarea', TOKEN);
await page.click('button[type="submit"]');

// Tree ready when the pinned Topology entry has painted.
await page.getByRole('button', { name: 'Topology' }).waitFor({ timeout: 25000 });
await page.waitForTimeout(800);
await snap('0-landing');

// The VM grid (VMs tab) — baseline.
await step('1-grid', async () => {
	await page.getByRole('button', { name: 'VMs', exact: true }).first().click();
	await page.waitForSelector('tbody tr', { timeout: 8000 });
});

// Topology map.
await step('2-topology', async () => {
	await page.getByRole('button', { name: 'Topology' }).click();
});

// New Segment modal (toggle topology off first so the header is clean).
await step('3-new-segment', async () => {
	await page.getByRole('button', { name: 'Topology' }).click();
	await page.locator('header').getByRole('button', { name: 'New', exact: true }).click();
	await page.getByRole('button', { name: 'New Segment' }).click();
});
await closeModal();

// Tier-1 egress firewall modal (right-click a repo-backed project).
await step('4-egress-firewall', async () => {
	await page.locator('aside').getByText('team-a', { exact: true }).first().click({ button: 'right' });
	await page.getByRole('button', { name: /New Egress Firewall/ }).click();
});
await closeModal();

// Dual-vocab segment Configure panel: Segments lens → a group → Configure.
await step('5-segment-configure', async () => {
	await page.getByRole('button', { name: 'Segments' }).click();
	await page.waitForTimeout(500);
	await page.locator('aside button.font-semibold, aside .font-semibold').filter({ hasText: /net|VM Network|udn|frontend|backend/i }).first().click();
	await page.getByRole('button', { name: 'Configure' }).click();
});

// Tier-0 service modal (New menu → New Tier-0 Service).
await step('6-tier0', async () => {
	await page.locator('header').getByRole('button', { name: 'New', exact: true }).click();
	await page.getByRole('button', { name: 'New Tier-0 Service' }).click();
});
await closeModal();

// Distributed Firewall modal (right-click a project → New Security Policy).
await step('7-dfw', async () => {
	await page.getByRole('button', { name: 'Projects' }).click();
	await page.waitForTimeout(300);
	await page.locator('aside').getByText('team-a', { exact: true }).first().click({ button: 'right' });
	await page.getByRole('button', { name: /New Security Policy/ }).click();
});
await closeModal();

// Admin (cluster-wide) firewall modal (New menu → New Admin Firewall).
await step('8-admin-fw', async () => {
	await page.locator('header').getByRole('button', { name: 'New', exact: true }).click();
	await page.getByRole('button', { name: 'New Admin Firewall' }).click();
});
await closeModal();

console.log('shots captured:', done.join(', '));
// Diagnostics: list header + lens buttons so we can fix selectors if a step missed.
const labels = await page.locator('header button, aside button').allInnerTexts();
console.log('buttons:', labels.map((s) => s.replace(/\s+/g, ' ').trim()).filter(Boolean).slice(0, 25).join(' | '));
await browser.close();

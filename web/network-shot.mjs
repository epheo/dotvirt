// Phase 6.2 visual-verification drive: VM network adapters (resolved port group +
// live IP/MAC), the Networks lens (friendly port-group grouping), a network
// detail card, and the node fabric (uplinks + physical adapters). Needs OC_TOKEN
// and the stack running (BASE_URL overrides the default Vite-dev origin).
import { chromium } from 'playwright';
const base = process.env.BASE_URL || 'http://localhost:5173';
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
page.setDefaultTimeout(30000);

async function step(name, fn) {
	try {
		await fn();
		console.log('OK   ' + name);
	} catch (e) {
		console.log('FAIL ' + name + ' :: ' + (e.message || e).split('\n')[0]);
	}
}

await page.goto(base);
await page.waitForSelector('textarea');
await page.fill('textarea', process.env.OC_TOKEN);
await page.click('button[type="submit"]');
await page.getByRole('button', { name: /New VM/ }).waitFor();
// The port-group catalog loads async after login; wait for it so resolveNIC has
// data (else the first VM's adapter renders the raw "Pod network" fallback).
await page.waitForResponse((r) => r.url().endsWith('/api/networks'), { timeout: 15000 }).catch(() => {});
await page.waitForTimeout(300);

await step('vm-adapters', async () => {
	await page.getByRole('button', { name: 'VMs', exact: true }).click();
	const row = page.locator('tbody tr').first();
	await row.waitFor();
	await row.click();
	await page.getByRole('button', { name: /Edit Settings/ }).waitFor();
	// VM-detail tabs render lowercase text via `capitalize` CSS, so the accessible
	// name is lowercase — match case-insensitively.
	await page.getByRole('button', { name: /^configure$/i }).click();
	await page.getByRole('button', { name: 'Network', exact: true }).click();
	await page.waitForSelector('text=Network adapters');
	await page.screenshot({ path: '/tmp/net-1-vm-adapters.png' });
});

await step('networks-lens', async () => {
	await page.getByRole('button', { name: 'Networks', exact: true }).click();
	await page.waitForSelector('text=network-a');
	await page.screenshot({ path: '/tmp/net-2-networks-lens.png' });
});

await step('network-detail', async () => {
	await page.getByText('network-a', { exact: true }).first().click();
	await page.getByRole('button', { name: 'Configure', exact: true }).click();
	await page.waitForSelector('text=Backing');
	await page.screenshot({ path: '/tmp/net-3-network-detail.png' });
});

await step('node-fabric', async () => {
	await page.getByRole('button', { name: 'Nodes', exact: true }).click();
	await page.getByText('hetznet', { exact: true }).first().click();
	await page.getByRole('button', { name: 'Configure', exact: true }).click();
	await page.waitForSelector('text=Physical adapters');
	await page.screenshot({ path: '/tmp/net-4-node-fabric.png' });
});

await browser.close();
console.log('done');

// Phase 5 verification: overcommit chips (5.3), node cordon/evacuate panel
// (5.1), console-preview thumbnail (5.2).
import { chromium } from 'playwright';
const TOKEN = process.env.OC_TOKEN;
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 950 } });
page.setDefaultTimeout(30000);
await page.goto('http://localhost:5173');
await page.waitForSelector('textarea');
await page.fill('textarea', TOKEN);
await page.click('button[type="submit"]');
await page.waitForSelector('text=vm-tenant-a', { timeout: 20000 });

// 5.3: overcommit chips on the All-VMs Summary.
await page.waitForSelector('text=Overcommit', { timeout: 15000 });
await page.waitForTimeout(800);
await page.screenshot({ path: '/tmp/phase5-1-overcommit.png' });
console.log('5.3 overcommit chips: shown');

// 5.1: Nodes lens → a node → Configure → Maintenance panel.
await page.getByRole('button', { name: 'Nodes', exact: true }).click();
await page.locator('aside').getByText('hetznet', { exact: true }).click();
await page.getByRole('button', { name: 'Configure', exact: true }).click();
await page.waitForSelector('text=Maintenance', { timeout: 10000 });
await page.waitForTimeout(500);
await page.screenshot({ path: '/tmp/phase5-2-node-maintenance.png' });
console.log('5.1 node maintenance panel: shown');

// 5.2: a running VM's Summary → console preview thumbnail.
await page.getByRole('button', { name: 'Projects', exact: true }).click();
await page.locator('aside').getByText('vm-tenant-a', { exact: true }).click();
await page.waitForSelector('text=Capacity', { timeout: 10000 });
// The screenshot img loads async; wait for it.
await page.waitForSelector('img[alt="Console preview"]', { timeout: 15000 });
await page.waitForTimeout(1500);
await page.screenshot({ path: '/tmp/phase5-3-console-preview.png' });
console.log('5.2 console preview: rendered');

await browser.close();
console.log('OK');

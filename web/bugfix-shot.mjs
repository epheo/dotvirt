import { chromium } from 'playwright';
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1600, height: 950 }, ignoreHTTPSErrors: true });
page.setDefaultTimeout(30000);
await page.goto('http://localhost:5173');
await page.waitForSelector('textarea');
await page.fill('textarea', process.env.OC_TOKEN);
await page.click('button[type="submit"]');
await page.waitForSelector('text=vm-tenant-a', { timeout: 20000 });
await page.locator('aside').getByText('vm-tenant-a', { exact: true }).click();
await page.waitForSelector('img[alt="Console preview"]', { timeout: 15000 });
await page.waitForTimeout(1500);
await page.screenshot({ path: '/tmp/fix-1-summary-layout.png' });
console.log('summary layout: captured');
// Snapshots tab → restore note + greyed restore.
await page.locator('main').getByRole('button', { name: /^snapshots$/i }).click();
await page.waitForSelector('text=/Restore is disabled while the VM is running/', { timeout: 10000 });
await page.waitForTimeout(800);
await page.screenshot({ path: '/tmp/fix-2-snapshot-note.png' });
console.log('snapshot note: shown');
// Catalog (dev backend kube:admin → populated).
await page.getByRole('button', { name: 'Catalog', exact: true }).click();
await page.waitForSelector('aside >> text=fedora', { timeout: 10000 });
await page.waitForTimeout(500);
await page.screenshot({ path: '/tmp/fix-3-catalog.png' });
console.log('catalog: populated');
await browser.close();
console.log('OK');

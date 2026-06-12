// 3.4 verification: per-NIC/per-drive series, IOPS chart, stacked memory, and
// the Month range on the VM Performance tab.
// Run: OC_TOKEN=$(oc whoami -t) node perf-shot.mjs
import { chromium } from 'playwright';

const TOKEN = process.env.OC_TOKEN;
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
page.setDefaultTimeout(30000);

await page.goto('http://localhost:5173');
await page.waitForSelector('textarea');
await page.fill('textarea', TOKEN);
await page.click('button[type="submit"]');
await page.waitForSelector('text=vm-tenant-a', { timeout: 20000 });

await page.getByRole('button', { name: 'VMs', exact: true }).click();
await page.locator('tbody tr', { hasText: 'vm-tenant-a' }).first().click();
await page.locator('main').getByRole('button', { name: 'monitor' }).click();
await page.locator('main').getByRole('button', { name: 'performance' }).click();
await page.waitForSelector('.uplot', { timeout: 30000 });
await page.waitForSelector('text=Disk IOPS');
await page.waitForTimeout(1200);
await page.screenshot({ path: '/tmp/perf-1-vm-charts.png', fullPage: true });
console.log('vm charts ok');

// Month range renders (retention-bounded data is fine).
await page.getByRole('button', { name: 'Month', exact: true }).click();
await page.waitForTimeout(2500);
await page.screenshot({ path: '/tmp/perf-2-month.png', fullPage: true });
console.log('month range ok');

await browser.close();
console.log('OK');

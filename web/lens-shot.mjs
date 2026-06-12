// 3.3 verification: Networks/Storage lenses + the wizard's storage-class
// select. Run: OC_TOKEN=$(oc whoami -t) node lens-shot.mjs
import { chromium } from 'playwright';

const TOKEN = process.env.OC_TOKEN;
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
page.setDefaultTimeout(30000);

await page.goto('http://localhost:5173');
await page.waitForSelector('textarea');
await page.fill('textarea', TOKEN);
await page.click('button[type="submit"]');
await page.waitForSelector('text=vm-tenant-a', { timeout: 20000 });

await page.getByRole('button', { name: 'Networks', exact: true }).click();
await page.waitForSelector('aside >> text=pod');
// Scope to the pod network to show grid filtering + breadcrumb.
await page.locator('aside').getByRole('button', { name: /^pod/ }).click();
await page.getByRole('button', { name: 'VMs', exact: true }).click();
await page.waitForTimeout(400);
await page.screenshot({ path: '/tmp/lens-1-networks.png' });
console.log('networks lens ok');

await page.getByRole('button', { name: 'Storage', exact: true }).click();
await page.waitForTimeout(400);
await page.screenshot({ path: '/tmp/lens-2-storage.png' });
console.log('storage lens ok');

await page.getByRole('button', { name: 'New VM' }).click();
await page.waitForSelector('text=Storage class');
await page.waitForTimeout(300);
await page.screenshot({ path: '/tmp/lens-3-wizard.png' });
console.log('wizard storage class ok');

await browser.close();
console.log('OK');

import { chromium } from 'playwright';
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
page.setDefaultTimeout(30000);
await page.goto('http://localhost:5173');
await page.waitForSelector('textarea');
await page.fill('textarea', process.env.OC_TOKEN);
await page.click('button[type="submit"]');
await page.waitForSelector('text=vm-tenant-a', { timeout: 20000 });
// Project scope → Configure (quota note under the project card).
await page.locator('aside').getByRole('button', { name: /team-a/ }).click();
await page.getByRole('button', { name: 'Configure', exact: true }).click();
await page.waitForSelector('text=No ResourceQuotas');
// Dock ALARMS tab.
await page.getByRole('button', { name: /^Alarms/ }).click();
await page.waitForSelector('text=No triggered alarms');
await page.waitForTimeout(400);
await page.screenshot({ path: '/tmp/quota-alarms.png' });
console.log('OK');
await browser.close();

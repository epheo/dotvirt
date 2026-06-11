import { chromium } from 'playwright';

const TOKEN = process.env.OC_TOKEN;
const BASE = 'http://localhost:5173';
const OUT = '/tmp';

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });

await page.goto(BASE);
await page.waitForSelector('textarea');
await page.fill('textarea', TOKEN);
await page.click('button[type="submit"]');
// The table header paints immediately, but rows only arrive with the WS
// inventory frame. Gate on an actual VM row so we never snap the empty
// "No VMs in scope" grid (a fixed delay races the WS / an HMR reload).
await page.waitForSelector('tbody tr', { timeout: 20000 });
await page.screenshot({ path: `${OUT}/icons-1-grid.png` });

// Drill into a VM to show the detail header icons (Edit/Delete) + back-bar arrow.
await page.locator('tbody tr').first().click();
await page.waitForSelector('text=All VMs', { timeout: 5000 });
await page.waitForTimeout(500);
await page.screenshot({ path: `${OUT}/icons-2-detail.png` });

// Open Edit Settings to show the modal close X + section chevrons.
await page.getByRole('button', { name: /Edit Settings/ }).click();
await page.waitForTimeout(500);
await page.screenshot({ path: `${OUT}/icons-3-editmodal.png` });

await browser.close();
console.log('OK');

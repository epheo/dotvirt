import { chromium } from 'playwright';
const browser = await chromium.launch({ args: ['--ignore-certificate-errors'] });
const page = await browser.newPage({ viewport: { width: 1440, height: 950 }, ignoreHTTPSErrors: true });
page.setDefaultTimeout(30000);
await page.goto('http://localhost:5173');
await page.waitForSelector('textarea');
await page.fill('textarea', process.env.OC_TOKEN);
await page.click('button[type="submit"]');
await page.waitForSelector('text=vm-tenant-a', { timeout: 20000 });
await page.getByRole('button', { name: 'Upload', exact: true }).click();
await page.waitForSelector('text=Upload image');
await page.setInputFiles('input[type=file]', '/tmp/test-upload.img');
await page.fill('input[placeholder="my-image"]', 'ui-upload-test');
await page.waitForTimeout(400);
await page.screenshot({ path: '/tmp/upload-1-form.png' });
console.log('form: filled');
// Drive the real upload (browser → proxy, cross-origin).
await page.getByRole('button', { name: 'Upload', exact: true }).last().click();
// Wait for it to reach Importing or Done (proves create→ready→stream→import).
await page.waitForSelector('text=/Importing|is ready/', { timeout: 120000 });
await page.waitForTimeout(500);
await page.screenshot({ path: '/tmp/upload-2-progress.png' });
await page.waitForSelector('text=is ready', { timeout: 120000 });
await page.waitForTimeout(300);
await page.screenshot({ path: '/tmp/upload-3-done.png' });
console.log('upload: done');
await browser.close();
console.log('OK');

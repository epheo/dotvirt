import { expect, login, setScenario, test } from './fx';

// A 300+ VM fleet (scenario: large): rendering, search and navigation must
// hold when every WS frame carries the whole tree. Catches the O(fleet)
// regressions a 3-VM dev cluster never shows.

test.beforeEach(async ({ page }) => {
	await setScenario(page, 'large');
	await login(page);
});

test('the tree and the all-VMs table render a 300-VM fleet', async ({ page }) => {
	const aside = page.locator('aside');
	await expect(aside.getByText('tenant-0', { exact: true })).toBeVisible();
	await expect(aside.getByText('tenant-11', { exact: true })).toBeVisible();

	// The table pages: one page renders, the footer says where it sits.
	const main = page.locator('main');
	await main.getByRole('link', { name: 'VMs', exact: true }).click();
	const grid = main.getByRole('grid', { name: 'VMs' });
	await expect(grid.locator('tbody tr')).toHaveCount(50);
	const pages = main.getByRole('navigation', { name: 'Pages' });
	await expect(pages).toContainText('Showing 1-50 of 312');
	await pages.getByRole('button', { name: 'Next page' }).click();
	await expect(pages).toContainText('Showing 51-100 of 312');
	await expect(pages).toContainText('Page 2 of 7');
});

test('selection reaches past the page; a filter starts over at page 1', async ({ page }) => {
	const main = page.locator('main');
	await main.getByRole('link', { name: 'VMs', exact: true }).click();
	const pages = main.getByRole('navigation', { name: 'Pages' });
	await pages.getByRole('button', { name: 'Next page' }).click();
	await main.getByLabel('Select this page').check();
	await main.getByRole('button', { name: 'Select all 312' }).click();
	await expect(page.getByText('312 selected')).toBeVisible();
	await main.getByRole('button', { name: 'Clear selection' }).click();

	await main.getByLabel('Filter VMs').fill('vm-11-');
	await expect(pages).toContainText('Showing 1-26 of 26');
	await expect(pages.getByRole('button', { name: 'Next page' })).toBeDisabled();
});

test('global search finds one VM among hundreds', async ({ page }) => {
	await page.keyboard.press('/');
	await page.keyboard.type('vm-11-1-7');
	await page.locator('li button', { hasText: 'vm-11-1-7' }).first().click();
	await expect(page).toHaveURL(/vm-11-1-7/);
	await expect(page.getByRole('button', { name: /Edit Settings/ })).toBeVisible();
});

test('hosts lens groups the fleet by node', async ({ page }) => {
	await page.locator('aside').getByRole('link', { name: 'Hosts' }).click();
	await expect(page.locator('aside').getByText('worker-1').first()).toBeVisible();
});

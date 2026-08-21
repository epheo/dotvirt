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

	const main = page.locator('main');
	await main.getByRole('link', { name: 'VMs', exact: true }).click();
	await expect(main.locator('tbody tr').first()).toBeVisible();
	const rows = await main.locator('tbody tr').count();
	expect(rows).toBeGreaterThanOrEqual(300);
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

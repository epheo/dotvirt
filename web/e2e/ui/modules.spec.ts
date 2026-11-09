import { expect, login, setScenario, test } from './fx';

const tiles = (page: import('@playwright/test').Page) =>
	page.locator('aside').first().locator('nav a');

test('without VMs Compute leaves, home lands on the next section', async ({ page }) => {
	await setScenario(page, 'novms');
	await login(page);
	await page.goto('/');
	await expect(tiles(page)).toHaveText(['Hosts', 'Networking', 'Storage', 'Changes']);
	await expect(page).toHaveURL(/\/hosts$/);
});

test('a project keeps its page without VMs, and only the root leaves', async ({ page }) => {
	await setScenario(page, 'novms');
	await login(page);
	await page.goto('/compute/team-web');
	await expect(page).toHaveURL(/\/compute\/team-web$/);
	const main = page.locator('main');
	await expect(main.getByRole('link', { name: 'Permissions' })).toBeVisible();
	await expect(main.getByRole('link', { name: /^VMs/ })).toHaveCount(0);
	await expect(page.locator('aside').first().getByText('All VMs')).toHaveCount(0);
	await page.goto('/compute');
	await expect(page).toHaveURL(/\/hosts$/);
});

test('without VMs the VM tabs leave Networking and Storage', async ({ page }) => {
	await setScenario(page, 'novms');
	await login(page);
	await page.goto('/networking');
	const main = page.locator('main');
	await expect(main.getByRole('link', { name: 'Map' })).toBeVisible();
	await expect(main.getByRole('link', { name: /^VMs/ })).toHaveCount(0);

	// A kept link lands on the page's default, not on an empty VM table.
	await page.goto('/networking?tab=vms');
	await expect(main.getByRole('link', { name: 'Map' })).toHaveAttribute('aria-current', 'page');

	await page.goto('/networking/blue-secondary');
	await expect(main.getByRole('heading', { name: 'Where it connects' })).toBeVisible();
	await expect(main.getByText('in a VM manifest')).toHaveCount(0);

	await page.goto('/storage');
	await expect(main.getByRole('link', { name: /^Classes/ })).toBeVisible();
	await expect(main.getByRole('link', { name: /^Library/ })).toHaveCount(0);
	await expect(main.getByRole('link', { name: /^Disks/ })).toHaveCount(0);
});

test('without VMs New opens on a network and offers no VM', async ({ page }) => {
	await setScenario(page, 'novms');
	await login(page);
	await page.goto('/hosts');
	await page.getByRole('button', { name: 'New', exact: true }).click();
	await expect(page.getByRole('radio', { name: 'Network', exact: true })).toBeChecked();
	await expect(page.getByRole('radio', { name: 'VM', exact: true })).toHaveCount(0);
	await expect(page.getByRole('radio', { name: 'Image', exact: true })).toHaveCount(0);
});

test('an unanswered module read keeps the whole console', async ({ page }) => {
	await setScenario(page, 'novms');
	await page.route('**/api/modules', (r) => r.abort());
	await login(page);
	await expect(tiles(page).first()).toHaveText('Compute');
});

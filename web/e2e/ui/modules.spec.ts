import { expect, login, setScenario, test } from './fx';

const tiles = (page: import('@playwright/test').Page) =>
	page.locator('aside').first().locator('nav a');

test('without VMs the first tile is Projects and home lists them', async ({ page }) => {
	await setScenario(page, 'novms');
	await login(page);
	await page.goto('/');
	await expect(tiles(page)).toHaveText(['Projects', 'Hosts', 'Networking', 'Storage', 'Changes']);
	await expect(page).toHaveURL(/\/compute$/);
	await expect(
		page
			.locator('aside')
			.first()
			.getByRole('link', { name: /^All projects/ }),
	).toHaveAttribute('aria-current', 'page');
	const main = page.locator('main');
	await main.getByRole('link', { name: 'team-web', exact: true }).click();
	await expect(page).toHaveURL(/\/compute\/team-web$/);
});

test('a project keeps its page without VMs, under All projects', async ({ page }) => {
	await setScenario(page, 'novms');
	await login(page);
	await page.goto('/compute/team-web');
	const main = page.locator('main');
	await expect(main.getByRole('link', { name: 'Permissions' })).toBeVisible();
	await expect(main.getByRole('link', { name: /^VMs/ })).toHaveCount(0);
	await expect(main.getByRole('link', { name: 'All projects', exact: true })).toBeVisible();
	await expect(main.getByText('All VMs')).toHaveCount(0);
	await expect(page.locator('aside').first().getByText('All VMs')).toHaveCount(0);
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

test('without VMs New on the Projects root opens on a project', async ({ page }) => {
	await setScenario(page, 'novms');
	await login(page);
	await page.goto('/compute');
	await page.getByRole('button', { name: 'New', exact: true }).click();
	await expect(page.getByRole('radio', { name: 'Project', exact: true })).toBeChecked();
});

test('an unanswered module read keeps the whole console', async ({ page }) => {
	await setScenario(page, 'novms');
	await page.route('**/api/modules', (r) => r.abort());
	await login(page);
	await expect(tiles(page).first()).toHaveText('Compute');
});

test('without project networks New Project neither offers nor stages one', async ({ page }) => {
	await setScenario(page, 'nonetworks');
	await login(page);
	await page.goto('/compute');
	await page.getByRole('button', { name: 'New', exact: true }).click();
	const sheet = page.getByRole('dialog');
	await sheet.getByRole('radio', { name: 'Project', exact: true }).click();
	await sheet.getByLabel('Name', { exact: true }).fill('team-c');
	await expect(sheet.getByRole('checkbox', { name: /default network/ })).toHaveCount(0);

	const posted = page.waitForRequest(
		(r) =>
			r.method() === 'POST' &&
			new URL(r.url()).pathname === '/api/projects' &&
			!r.url().includes('dryRun'),
	);
	await sheet.getByRole('button', { name: 'Stage project' }).click();
	const body = (await posted).postDataJSON();
	expect(body).toEqual({ name: 'team-c', namespace: 'team-c' });
});

test('without OLM rebalancing says the cluster cannot install it, never a missing right', async ({
	page,
}) => {
	await setScenario(page, 'noolm');
	await login(page);
	await page.goto('/hosts?tab=configure');
	const main = page.locator('main');
	await expect(
		main.getByText(
			'This cluster has no operator catalog (OLM), so rebalancing cannot be installed',
		),
	).toBeVisible();
	await expect(main.getByRole('button', { name: 'Enable Dynamic Rescheduling' })).toHaveCount(0);
	await expect(main.getByText(/You may not/)).toHaveCount(0);
});

test('without MachineConfig rebalancing offers no PSI', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	for (const [scenario, offered] of [
		['base', 1],
		['nomachineconfig', 0],
	] as const) {
		await setScenario(page, scenario);
		await page.goto('/hosts?tab=configure');
		await page.getByRole('button', { name: 'Enable Dynamic Rescheduling' }).click();
		const dialog = page.getByRole('dialog');
		await expect(
			dialog.getByRole('heading', { name: 'Configure Dynamic Rescheduling' }),
		).toBeVisible();
		await expect(dialog.getByText('Enable PSI on worker nodes')).toHaveCount(offered);
	}
});

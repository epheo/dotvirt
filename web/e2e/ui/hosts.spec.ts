import { expect, login, setScenario, test } from './fx';

// The Hosts section: the root says which hosts need a person and lists the
// fleet; a host's page carries its state and its maintenance verbs, which run
// now, in its header.

test('the hosts root names the hosts that need a person', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/hosts');
	const main = page.locator('main');
	await expect(main.getByRole('navigation', { name: 'Breadcrumb' })).toHaveText(
		/Hosts\s*\/\s*Summary/,
	);
	await expect(main.getByRole('heading', { level: 1, name: 'Hosts' })).toBeVisible();
	await expect(main.getByText('1 host in maintenance')).toBeVisible();
	await expect(main.getByText('3 hosts, 4 VMs')).toBeVisible();
	await expect(main.getByRole('link', { name: 'VMs 4' })).toBeVisible();
	await expect(main.getByRole('heading', { name: 'All hosts' })).toBeVisible();
	await expect(page.locator('aside').getByText('All hosts')).toBeVisible();
	// Nothing is a problem in base: no count tiles.
	await expect(main.getByText(/hosts? not ready/)).toHaveCount(0);
});

test('a host that is not ready, or near full, is counted with the way to it', async ({ page }) => {
	await setScenario(page, 'hosttrouble');
	await login(page);
	await page.goto('/hosts');
	const main = page.locator('main');
	await expect(main.getByText('1 host not ready, 1 in maintenance')).toBeVisible();
	await expect(main.getByText('host above 90% memory')).toBeVisible();
	// The tile comes before the table's own row link.
	await main.getByRole('link', { name: 'worker-2', exact: true }).first().click();
	await expect(page).toHaveURL(/\/hosts\/worker-2$/);
	await expect(main.getByRole('heading', { level: 1, name: 'worker-2' })).toBeVisible();
	await expect(main.getByText('Not ready', { exact: true })).toBeVisible();
});

test("a host's maintenance verbs run now, from its header", async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/hosts/worker-1');
	const main = page.locator('main');
	const crumbs = main.getByRole('navigation', { name: 'Breadcrumb' });
	await expect(crumbs.getByRole('link', { name: 'Hosts' })).toHaveAttribute('href', '/hosts');
	await expect(crumbs).toContainText('worker-1');
	await expect(main.getByText('Ready', { exact: true })).toBeVisible();
	await expect(main.getByText('Runs now')).toBeVisible();

	await main.getByRole('button', { name: 'Cordon' }).click();
	await expect(page.getByText('worker-1 cordoned. No new VMs land there.')).toBeVisible();
	await expect(main.getByText('Cordoned', { exact: true })).toBeVisible();
	await expect(main.getByRole('button', { name: 'Uncordon' })).toBeVisible();

	// Maintenance asks once more, in place, saying what it moves.
	await main.getByRole('button', { name: 'Enter maintenance' }).click();
	await expect(
		main.getByText(/Cordons worker-1 and live-migrates its 2 running VMs/),
	).toBeVisible();
	await main.getByRole('button', { name: 'Confirm maintenance' }).click();
	await expect(
		page.getByText('worker-1 entering maintenance: migration requested for 2 VMs.'),
	).toBeVisible();
	await expect(main.getByText('Maintenance', { exact: true })).toBeVisible();
	await expect(main.getByRole('button', { name: /Retry evacuation/ })).toBeVisible();

	await main.getByRole('button', { name: 'Exit maintenance' }).click();
	await expect(main.getByText('Ready', { exact: true })).toBeVisible();
	await expect(main.getByRole('button', { name: 'Enter maintenance' })).toBeVisible();
});

test("a host's Configure tab lists its fabric", async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/hosts/worker-1?tab=configure');
	const main = page.locator('main');
	await expect(main.getByRole('heading', { name: 'Uplinks' })).toBeVisible();
	await expect(main.getByRole('heading', { name: 'Physical adapters' })).toBeVisible();
	await expect(
		main.getByText('Host configuration is managed by the cluster platform'),
	).toBeVisible();
});

test('a caller who cannot list nodes gets no Hosts section and no dead host link', async ({
	page,
}) => {
	await setScenario(page, 'tenant');
	await login(page);
	let reads = 0;
	page.on('request', (r) => {
		if (new URL(r.url()).pathname === '/api/nodes') reads++;
	});
	const main = page.locator('main');
	const tiles = page.locator('aside').first().locator('nav a');
	await expect(tiles).toHaveText(['Compute', 'Networking', 'Storage', 'Changes']);
	await expect(main.getByRole('link', { name: 'See hosts' })).toHaveCount(0);

	// A link kept from before lands on Compute and says why.
	await page.goto('/hosts/worker-1');
	await expect(page).toHaveURL(/\/compute$/);
	await expect(page.getByText('You may not list hosts.')).toBeVisible();

	// A VM's host is named, not linked.
	await page.goto('/vm/web-prod/web-1');
	await expect(main.getByText(/ on worker-\d/).first()).toBeVisible();
	await expect(main.locator('a[href^="/hosts"]')).toHaveCount(0);
	expect(reads).toBe(0);
});

test('an unanswered capability read offers no Hosts tile and keeps the page', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.route('**/api/capabilities*', (r) => r.abort());
	await page.goto('/hosts');
	await expect(
		page.locator('main').getByText('Your access could not be checked. Try again in a moment.'),
	).toBeVisible();
	await expect(page).toHaveURL(/\/hosts$/);
	await expect(page.locator('aside').first().getByRole('link', { name: 'Hosts' })).toHaveCount(0);
});

import { expect, login, openTray, setScenario, test } from './fx';

// Every section root is a container with the same breadcrumb + tab chrome;
// roots differ only in what their Summary shows. Non-object views (the policy
// plane, the review route) are a tab or a mode, never a sibling page.

test('hosts root is a fleet table, not the compute cards', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/hosts');
	const main = page.locator('main');
	await expect(main.getByRole('link', { name: 'Summary', exact: true })).toBeVisible();
	await expect(main.getByRole('link', { name: 'Configure', exact: true })).toBeVisible();
	await expect(main.getByRole('link', { name: 'Permissions', exact: true })).toHaveCount(0);
	await expect(main.getByRole('columnheader', { name: 'Host' })).toBeVisible();
	await expect(main.getByRole('columnheader', { name: 'vCPU committed' })).toBeVisible();
	const row = main.locator('tbody tr', { hasText: 'worker-3' });
	await expect(row).toContainText('Maintenance');
	// The host balance card lives here; its rebalancing link lands on this root's Configure.
	await expect(main.getByText('Host balance').first()).toBeVisible();
	await main.getByRole('link', { name: 'Configure', exact: true }).click();
	await expect(main.getByText('Dynamic Rescheduling').first()).toBeVisible();
	// A host row opens the node's object page with the same chrome.
	await page.goto('/hosts');
	await main.locator('tbody').getByRole('link', { name: 'worker-1' }).click();
	await expect(page).toHaveURL(/\/hosts\/worker-1$/);
	await expect(main.getByRole('link', { name: 'Monitor', exact: true })).toBeVisible();
	// The Monitor card keeps one title; the toggle in its header picks the view.
	await main.getByRole('link', { name: 'Monitor', exact: true }).click();
	const monitor = main.getByRole('region', { name: 'Monitor' });
	await monitor.getByRole('button', { name: 'Performance' }).click();
	await expect(monitor.getByRole('button', { name: 'Performance' })).toHaveAttribute(
		'aria-pressed',
		'true',
	);
	await expect(main.getByRole('heading', { name: 'Performance' })).toHaveCount(0);
});

test('compute root no longer carries the host cards', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/compute');
	const main = page.locator('main');
	await expect(main.getByText('Top consumers').first()).toBeVisible();
	await expect(main.getByText('Host balance')).toHaveCount(0);
	await expect(main.getByText('Host capacity')).toHaveCount(0);
});

test('networking root: the map is the first tab, the rules are the Firewall tab', async ({
	page,
}) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/networking');
	const main = page.locator('main');
	await expect(main.getByRole('heading', { name: 'Cluster map' })).toBeVisible();
	// The VMs tab is a card on the grey ground, as on Compute and Hosts.
	await main.getByRole('link', { name: /^VMs/ }).click();
	await expect(main.getByRole('region', { name: 'Networking, VMs' })).toBeVisible();
	await main.getByRole('link', { name: /^Firewall/ }).click();
	await expect(page).toHaveURL(/\/networking\?tab=firewall$/);
	await expect(main.getByRole('button', { name: 'Check connectivity' })).toBeVisible();
	// The tree has no Security row: the map, networks by kind and the uplinks.
	await expect(page.locator('aside').getByRole('link', { name: 'Map' })).toBeVisible();
	await expect(page.locator('aside a[href="/networking/security"]')).toHaveCount(0);
});

test('the old security path redirects and keeps its tenant', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/networking/security?tenant=team-web');
	await expect(page).toHaveURL(/\/networking\?tab=firewall&tenant=team-web$/);
	await expect(page.getByLabel('Filter by tenant')).toHaveValue('team-web');
});

test('templates: a tab of Compute, with a rail row; the open template rides the URL', async ({
	page,
}) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/compute');
	const main = page.locator('main');
	const rail = page.locator('aside').first();
	await rail.getByRole('link', { name: 'VM templates' }).click();
	await expect(page).toHaveURL(/\/compute\?tab=templates$/);
	await expect(main.getByText('In git, under templates/ in each library repo')).toBeVisible();
	await expect(main.locator('tbody tr')).toHaveCount(6);

	await main.getByRole('button', { name: 'web-node', exact: true }).click();
	await expect(page).toHaveURL(/tab=templates&item=team-web%2Fweb-node$/);
	const panel = main.locator('aside');
	await expect(panel.getByRole('heading', { name: 'web-node' })).toBeVisible();
	await expect(panel.getByRole('button', { name: 'Edit template' })).toBeEnabled();
	// A reload keeps the selection: the item is URL state, not component state.
	await page.reload();
	await expect(panel.getByRole('heading', { name: 'web-node' })).toBeVisible();

	// All VMs leaves the tab; every other rail row keeps it.
	await rail.getByRole('link', { name: /^All VMs/ }).click();
	await expect(page).toHaveURL(/\/compute\?tab=summary$/);
	await expect(main.getByText('Issues in All VMs')).toBeVisible();
});

test('templates: a project lists its own library and the shared one', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/compute/team-web?tab=templates');
	const main = page.locator('main');
	await expect(main.getByRole('button', { name: 'web-node', exact: true })).toBeVisible();
	await expect(main.getByRole('button', { name: 'fedora-server', exact: true })).toBeVisible();

	await page.goto('/compute/team-db?tab=templates');
	await expect(main.getByRole('button', { name: 'fedora-server', exact: true })).toBeVisible();
	await expect(main.getByRole('button', { name: 'web-node', exact: true })).toHaveCount(0);

	// A library belongs to a project: a namespace has no such tab.
	await page.goto('/compute/team-web/web-prod');
	await expect(main.getByRole('link', { name: 'Templates' })).toHaveCount(0);
});

test('templates: Create VM opens the sheet with the template picked', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/compute?tab=templates');
	await page.locator('main').getByRole('button', { name: 'Create VM from web-node' }).click();
	const sheet = page.getByRole('dialog');
	await expect(sheet.getByRole('heading', { name: 'New VM from template' })).toBeVisible();
	await expect(sheet.getByRole('radio', { name: /web-node/ })).toBeChecked();
});

test('the switcher has five tiles, and links to /catalog open Templates', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/compute');
	const tiles = page.locator('aside').first().locator('nav a');
	await expect(tiles).toHaveText(['Compute', 'Hosts', 'Networking', 'Storage', 'Changes']);

	await page.goto('/catalog');
	await expect(page).toHaveURL(/\/compute\?tab=templates$/);
	await page.goto('/catalog?kind=templates&item=team-web%2Fweb-node');
	await expect(page).toHaveURL(/\/compute\?tab=templates&item=team-web%2Fweb-node$/);
	await expect(page.locator('main aside').getByRole('heading', { name: 'web-node' })).toBeVisible();
	await page.goto('/catalog?kind=instancetypes&item=u1.medium');
	await expect(page).toHaveURL(/\/compute\?tab=templates$/);
});

test('Changes is a section: the tree scopes the review, a VM opens its own tab', async ({
	page,
}) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/changes');
	const aside = page.locator('aside');
	const main = page.locator('main');
	// The section is the full view of the pipeline, so the change tray steps aside.
	await expect(page.getByRole('complementary', { name: 'Changes in flight' })).toHaveCount(0);
	await expect(aside.getByText('All changes')).toBeVisible();
	// Both projects' PRs at the root; one project's under its scope, with its
	// history open without a click.
	await expect(main.getByRole('button', { name: /PR #41/ })).toBeVisible();
	await aside.locator('a[href="/changes/team-web"]').click();
	await expect(page).toHaveURL(/\/changes\/team-web$/);
	await expect(main.getByRole('button', { name: /PR #42/ })).toBeVisible();
	await expect(main.getByRole('button', { name: /PR #41/ })).toHaveCount(0);
	await main.getByRole('link', { name: /^History/ }).click();
	await expect(main.getByRole('button', { name: /web-2: add data disk/ })).toBeVisible();
	// A namespace narrows history to its directory: db-prod saw only the import.
	// The tree keeps the tab across scopes.
	await aside.locator('a[href="/changes/team-db?tab=history"]').click();
	await main.getByRole('link', { name: /^In review/ }).click();
	await expect(main.getByRole('button', { name: /PR #41/ })).toBeVisible();
	await main.getByRole('link', { name: /^History/ }).click();
	await aside.locator('a[href="/changes/team-db/db-prod?tab=history"]').click();
	await expect(page).toHaveURL(/\/changes\/team-db\/db-prod\?tab=history$/);
	await expect(main.getByRole('button', { name: /initial import/ })).toBeVisible();
	await expect(main.getByRole('button', { name: /web-2: add data disk/ })).toHaveCount(0);
	// A VM leaf opens the VM page on its Changes tab, keeping this tree.
	await aside.locator('a[href="/vm/web-prod/web-2?tab=changes"]').click();
	await expect(page).toHaveURL(/\/vm\/web-prod\/web-2\?tab=changes$/);
	await expect(aside.getByText('All changes')).toBeVisible();
	await expect(main.getByText('Current version')).toBeVisible();
	// Restore is the object-level undo: it stages, and the tray's lane opens
	// that project's draft.
	await main.getByRole('button', { name: 'Restore this version' }).click();
	await expect(page.getByText(/Restore of web-2 to 99fe210a staged/)).toBeVisible();
	const propose = (await openTray(page)).getByRole('link', { name: 'Open in Changes' });
	await expect(propose).toHaveAttribute('href', '/changes/team-web');
	await expect(main.getByText(/part of your team-web draft/)).toBeVisible();
	await propose.click();
	await expect(page).toHaveURL(/\/changes\/team-web$/);
	await expect(main.getByText('version 99fe210a')).toBeVisible();
});

test('the tree and breadcrumb keep the tab in view across objects', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/compute?tab=vms');
	const aside = page.locator('aside');
	const main = page.locator('main');
	await aside.locator('a[href="/compute/team-web?tab=vms"]').click();
	await expect(page).toHaveURL(/\/compute\/team-web\?tab=vms$/);
	await expect(main.getByText('web-1')).toBeVisible();
	// Walking up the breadcrumb keeps it too.
	await main.getByRole('link', { name: 'All VMs', exact: true }).first().click();
	await expect(page).toHaveURL(/\/compute\?tab=vms$/);
	// VM to VM on a VM-only tab stays on it.
	await page.goto('/vm/web-prod/web-1?tab=snapshots');
	await aside.locator('a[href="/vm/web-prod/web-2?tab=snapshots"]').click();
	await expect(page).toHaveURL(/\/vm\/web-prod\/web-2\?tab=snapshots$/);
	// A target without the tab lands on its Summary.
	await page.goto('/networking?tab=firewall');
	await aside.locator('a[href^="/networking/"]').first().click();
	await expect(page).not.toHaveURL(/tab=/);
	await expect(main.getByRole('heading', { name: 'Where it connects' })).toBeVisible();
});

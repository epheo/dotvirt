import { expect, login, setScenario, test } from './fx';

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
	// The host balance card lives here; its DRS link lands on this root's Configure.
	await expect(main.getByText('Host balance').first()).toBeVisible();
	await main.getByRole('link', { name: 'Configure', exact: true }).click();
	await expect(main.getByText('Dynamic Rescheduling').first()).toBeVisible();
	// A host row opens the node's object page with the same chrome.
	await page.goto('/hosts');
	await main.locator('tbody').getByRole('link', { name: 'worker-1' }).click();
	await expect(page).toHaveURL(/\/hosts\/worker-1$/);
	await expect(main.getByRole('link', { name: 'Monitor', exact: true })).toBeVisible();
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

test('networking root: topology is Summary, the policy plane is a tab', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/networking');
	const main = page.locator('main');
	await expect(main.getByText('Provider Gateway').first()).toBeVisible();
	await expect(main.getByRole('link', { name: 'VMs', exact: true })).toBeVisible();
	await main.getByRole('link', { name: 'Security', exact: true }).click();
	await expect(page).toHaveURL(/\/networking\?tab=security$/);
	await expect(main.getByRole('button', { name: 'Trace flow' })).toBeVisible();
	// The tree has no Security row any more: only the root and the segments.
	await expect(page.locator('aside').getByText('All Networks')).toBeVisible();
	await expect(page.locator('aside a[href="/networking/security"]')).toHaveCount(0);
});

test('the old security path redirects and keeps its tenant', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/networking/security?tenant=team-web');
	await expect(page).toHaveURL(/\/networking\?tab=security&tenant=team-web$/);
	await expect(page.getByLabel('Filter by tenant')).toHaveValue('team-web');
});

test('catalog: kinds are tabs, the tree lists items, selection rides the URL', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/catalog');
	const main = page.locator('main');
	const aside = page.locator('aside');
	await expect(main.getByRole('link', { name: 'Instance types', exact: true })).toBeVisible();
	await main.getByRole('link', { name: 'Instance types', exact: true }).click();
	await expect(page).toHaveURL(/\/catalog\?kind=instancetypes$/);
	// The tree carries the kind's items; clicking one selects it in the pane.
	const item = aside.locator('a[href^="/catalog?kind=instancetypes&item="]').first();
	const name = (await item.textContent())!.trim();
	await item.click();
	await expect(page).toHaveURL(new RegExp(`item=${encodeURIComponent(name)}$`));
	await expect(main.locator('aside').getByRole('heading', { name })).toBeVisible();
	// A reload keeps the selection: the item is URL state, not component state.
	await page.reload();
	await expect(main.locator('aside').getByRole('heading', { name })).toBeVisible();
});

test('Changes is a section: the tree scopes the review, a VM opens its own tab', async ({
	page,
}) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/changes');
	const aside = page.locator('aside');
	const main = page.locator('main');
	// No draft: no Propose button. The section is the place, the button the action.
	await expect(page.getByRole('link', { name: /Propose \d+ change/ })).toHaveCount(0);
	await expect(aside.getByText('All changes')).toBeVisible();
	// Both projects' PRs at the root; one project's under its scope, with its
	// history open without a click.
	await expect(main.getByRole('button', { name: /PR #41/ })).toBeVisible();
	await aside.locator('a[href="/changes/team-web"]').click();
	await expect(page).toHaveURL(/\/changes\/team-web$/);
	await expect(main.getByRole('button', { name: /PR #42/ })).toBeVisible();
	await expect(main.getByRole('button', { name: /PR #41/ })).toHaveCount(0);
	await expect(main.getByRole('button', { name: /web-2: add data disk/ })).toBeVisible();
	// A namespace narrows history to its directory: db-prod saw only the import.
	await aside.locator('a[href="/changes/team-db"]').click();
	await expect(main.getByRole('button', { name: /PR #41/ })).toBeVisible();
	await aside.locator('a[href="/changes/team-db/db-prod"]').click();
	await expect(page).toHaveURL(/\/changes\/team-db\/db-prod$/);
	await expect(main.getByRole('button', { name: /initial import/ })).toBeVisible();
	await expect(main.getByRole('button', { name: /web-2: add data disk/ })).toHaveCount(0);
	// A VM leaf opens the VM page on its Changes tab, keeping this tree.
	await aside.locator('a[href="/vm/web-prod/web-2?tab=changes"]').click();
	await expect(page).toHaveURL(/\/vm\/web-prod\/web-2\?tab=changes$/);
	await expect(aside.getByText('All changes')).toBeVisible();
	await expect(main.getByText('Current version')).toBeVisible();
	// Restore is the object-level undo: it stages, and the Propose button lands
	// on that project's lane.
	await main.getByRole('button', { name: 'Restore this version' }).click();
	await expect(page.getByText(/Restore of web-2 to 99fe210a staged/)).toBeVisible();
	const propose = page.getByRole('link', { name: /Propose 1 change/ });
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
	await page.goto('/networking?tab=security');
	await aside.locator('a[href^="/networking/"]').first().click();
	await expect(page).not.toHaveURL(/tab=/);
	await expect(main.getByText('VMs attached')).toBeVisible();
});

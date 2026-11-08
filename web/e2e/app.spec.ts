import { expect, test } from '@playwright/test';
import { login, openVM } from './helpers';

test.beforeEach(async ({ page }) => {
	await login(page);
});

test('shell + container workspace renders after login', async ({ page }) => {
	// New opens the create sheet. Enabled, not just visible: it stays disabled
	// until the first inventory frame proves a project the user may change,
	// so this also waits out the login race.
	await expect(page.getByRole('button', { name: /^New$/ })).toBeEnabled();
	await page.getByRole('button', { name: /^New$/ }).click();
	await expect(
		page.getByRole('dialog').getByRole('radio', { name: 'VM', exact: true }),
	).toBeChecked();
	await page.keyboard.press('Escape');
	await expect(
		page.locator('aside').getByRole('link', { name: 'Changes', exact: true }),
	).toBeVisible();
	// The All-VMs landing is a tabbed workspace (Summary / VMs / Monitor); tabs
	// are links (?tab=), scoped to main — the tree carries links of its own.
	const main = page.locator('main');
	await expect(main.getByRole('link', { name: 'Summary', exact: true })).toBeVisible();
	await expect(main.getByRole('link', { name: /^VMs [\d,]+$/ })).toBeVisible();
	await expect(main.getByRole('link', { name: 'Monitor', exact: true })).toBeVisible();
});

test('inventory sections switch tree and workspace', async ({ page }) => {
	const aside = page.locator('aside');
	await expect(aside.getByRole('link', { name: 'Compute' })).toBeVisible();
	await aside.getByRole('link', { name: 'Hosts' }).click();
	await expect(page).toHaveURL(/\/hosts$/);
	// The Hosts tree roots at All hosts and groups VMs by host.
	await expect(aside.getByText('All hosts')).toBeVisible();
	await aside.getByRole('link', { name: 'Storage', exact: true }).click();
	await expect(page).toHaveURL(/\/storage$/);
	await expect(aside.getByText('All projects')).toBeVisible();
});

test('VMs tab lists VMs and opens a detail route', async ({ page }) => {
	await openVM(page);
	await expect(page).toHaveURL(/\/vm\//);
	// VM workspace tabs are links: Summary / Monitor / … / Snapshots / Console.
	await expect(page.getByRole('link', { name: 'Snapshots', exact: true })).toBeVisible();
	await expect(page.getByRole('link', { name: 'Console', exact: true })).toBeVisible();
});

test('VM Monitor exposes the Events + Performance sub-rail', async ({ page }) => {
	await openVM(page);
	// Scope to the detail pane — the bottom dock also has an "Events" tab.
	const detail = page.locator('main');
	await detail.getByRole('link', { name: 'Monitor', exact: true }).click();
	await expect(detail.getByRole('button', { name: /^events$/i })).toBeVisible();
	await expect(detail.getByRole('button', { name: /^performance$/i })).toBeVisible();
});

test('Snapshots tab shows the take control', async ({ page }) => {
	await openVM(page);
	await page.getByRole('link', { name: 'Snapshots', exact: true }).click();
	await expect(page).toHaveURL(/tab=snapshots/);
	await expect(page.getByRole('button', { name: /Take snapshot/ })).toBeVisible();
});

test('views are deep-linkable and refresh-safe', async ({ page }) => {
	// Catalog is a routed workspace, not a drawer.
	await page.goto('/catalog?kind=instancetypes');
	await expect(page.getByText('Read-only — these are platform objects')).toBeVisible();
	// The map is the Networking root's first tab.
	await page.goto('/networking');
	await expect(page.getByRole('heading', { name: 'Cluster map' })).toBeVisible();
	// A network in the tree opens its own page.
	await page.locator('aside a[href^="/networking/"]').first().click();
	await expect(
		page.locator('main').getByRole('heading', { name: 'Where it connects' }),
	).toBeVisible();
	// A VM URL survives a hard reload (session cookie + fallback routing).
	await page.goto('/compute');
	await openVM(page);
	const url = page.url();
	await page.reload();
	await expect(page).toHaveURL(url);
	await expect(page.getByRole('button', { name: /Edit Settings/ })).toBeVisible();
});

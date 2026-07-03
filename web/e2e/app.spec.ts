import { expect, test } from '@playwright/test';
import { login, openFirstVM } from './helpers';

test.beforeEach(async ({ page }) => {
	await login(page);
});

test('shell + container workspace renders after login', async ({ page }) => {
	// Creation collapses into the "+ New" header menu; its items exist only while open.
	await page.getByRole('button', { name: /^New$/ }).click();
	await expect(page.getByRole('button', { name: /New VM/ })).toBeVisible();
	await page.keyboard.press('Escape');
	await expect(page.getByRole('button', { name: /Changes/ })).toBeVisible();
	// The All-VMs landing is a tabbed workspace (Summary / VMs / Monitor); tabs
	// are links (?tab=), scoped to main — the tree carries links of its own.
	const main = page.locator('main');
	await expect(main.getByRole('link', { name: 'Summary', exact: true })).toBeVisible();
	await expect(main.getByRole('link', { name: 'VMs', exact: true })).toBeVisible();
	await expect(main.getByRole('link', { name: 'Monitor', exact: true })).toBeVisible();
});

test('inventory sections switch tree and workspace', async ({ page }) => {
	const aside = page.locator('aside');
	await expect(aside.getByRole('link', { name: 'Compute' })).toBeVisible();
	await aside.getByRole('link', { name: 'Hosts' }).click();
	await expect(page).toHaveURL(/\/hosts$/);
	// The Hosts tree roots at All Nodes and groups VMs by node.
	await expect(aside.getByText('All Nodes')).toBeVisible();
	await aside.getByRole('link', { name: 'Storage', exact: true }).click();
	await expect(page).toHaveURL(/\/storage$/);
	await expect(aside.getByText('All Storage')).toBeVisible();
});

test('VMs tab lists VMs and opens a detail route', async ({ page }) => {
	await openFirstVM(page);
	await expect(page).toHaveURL(/\/vm\//);
	// VM workspace tabs are links: Summary / Monitor / … / Snapshots / Console.
	await expect(page.getByRole('link', { name: 'Snapshots', exact: true })).toBeVisible();
	await expect(page.getByRole('link', { name: 'Console', exact: true })).toBeVisible();
});

test('VM Monitor exposes the Events + Performance sub-rail', async ({ page }) => {
	await openFirstVM(page);
	// Scope to the detail pane — the bottom dock also has an "Events" tab.
	const detail = page.locator('main');
	await detail.getByRole('link', { name: 'Monitor', exact: true }).click();
	await expect(detail.getByRole('button', { name: /^events$/i })).toBeVisible();
	await expect(detail.getByRole('button', { name: /^performance$/i })).toBeVisible();
});

test('Snapshots tab shows the take control', async ({ page }) => {
	await openFirstVM(page);
	await page.getByRole('link', { name: 'Snapshots', exact: true }).click();
	await expect(page).toHaveURL(/tab=snapshots/);
	await expect(page.getByRole('button', { name: /Take snapshot/ })).toBeVisible();
});

test('views are deep-linkable and refresh-safe', async ({ page }) => {
	// Catalog is a routed workspace, not a drawer.
	await page.goto('/catalog?kind=instancetypes');
	await expect(page.getByText('Read-only — these are platform objects')).toBeVisible();
	// Topology is the Networking section home.
	await page.goto('/networking');
	await expect(page.getByText('provider edge').first()).toBeVisible();
	// A segment group in the tree opens its object page (Summary fact sheet).
	await page.locator('aside a[href^="/networking/"]').first().click();
	await expect(page.locator('main').getByText('VMs attached')).toBeVisible();
	// A VM URL survives a hard reload (session cookie + fallback routing).
	await page.goto('/compute');
	await openFirstVM(page);
	const url = page.url();
	await page.reload();
	await expect(page).toHaveURL(url);
	await expect(page.getByRole('button', { name: /Edit Settings/ })).toBeVisible();
});

import { expect, test } from '@playwright/test';
import { login, openFirstVM } from './helpers';

test.beforeEach(async ({ page }) => {
	await login(page);
});

test('shell + container workspace renders after login', async ({ page }) => {
	await expect(page.getByRole('button', { name: /New VM/ })).toBeVisible();
	await expect(page.getByRole('button', { name: /Changes/ })).toBeVisible();
	// The All-VMs landing is a tabbed workspace (Summary / VMs / Monitor).
	await expect(page.getByRole('button', { name: 'Summary', exact: true })).toBeVisible();
	await expect(page.getByRole('button', { name: 'VMs', exact: true })).toBeVisible();
	await expect(page.getByRole('button', { name: 'Monitor', exact: true })).toBeVisible();
});

test('inventory lenses switch between Projects and Nodes', async ({ page }) => {
	await expect(page.getByRole('button', { name: 'Projects' })).toBeVisible();
	await page.getByRole('button', { name: 'Nodes' }).click();
	// Lens toggle persists and the tree is still anchored on the All-VMs root.
	await expect(page.getByRole('button', { name: 'Nodes' })).toBeVisible();
	await expect(page.getByText('All VMs').first()).toBeVisible();
});

test('VMs tab lists VMs and opens a detail view', async ({ page }) => {
	await openFirstVM(page);
	// VM workspace: Summary / Monitor / Snapshots / Console tabs (lowercase in DOM).
	await expect(page.getByRole('button', { name: /^snapshots$/i })).toBeVisible();
	await expect(page.getByRole('button', { name: /^console$/i })).toBeVisible();
});

test('VM Monitor exposes the Events + Performance sub-rail', async ({ page }) => {
	await openFirstVM(page);
	// Scope to the detail pane — the bottom dock also has an "EVENTS" tab.
	const detail = page.locator('main');
	await detail.getByRole('button', { name: /^monitor$/i }).click();
	await expect(detail.getByRole('button', { name: /^events$/i })).toBeVisible();
	await expect(detail.getByRole('button', { name: /^performance$/i })).toBeVisible();
});

test('Snapshots tab shows the take control', async ({ page }) => {
	await openFirstVM(page);
	await page.getByRole('button', { name: /^snapshots$/i }).click();
	await expect(page.getByRole('button', { name: /Take snapshot/ })).toBeVisible();
});

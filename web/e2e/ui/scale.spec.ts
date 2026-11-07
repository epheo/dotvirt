import { expect, login, setScenario, test } from './fx';

// The full platform the boards draw (scenario: scale, 2,846 VMs on 48 hosts):
// every section boots and renders it without a console error. Screens built
// for a few VMs fall over here first, so this walk guards them all.

test.describe.configure({ timeout: 120_000 });

test.beforeEach(async ({ page }) => {
	await setScenario(page, 'scale');
	await login(page);
});

test('every section renders the full platform', async ({ page }) => {
	const main = page.locator('main');
	const aside = page.locator('aside');

	await page.goto('/compute');
	await expect(aside.getByText('team-batch', { exact: true })).toBeVisible();
	await expect(main.getByText('Top consumers').first()).toBeVisible();

	await page.goto('/compute?tab=vms');
	await expect(main.locator('tbody tr').first()).toBeVisible();

	await page.goto('/hosts');
	await expect(main.locator('tbody tr', { hasText: 'host-17' })).toContainText('Maintenance');

	await page.goto('/networking');
	await expect(main.getByText('2 problems')).toBeVisible();
	await expect(main.getByText('47 more projects')).toBeVisible();
	await page.goto('/networking/web-prod/web-net');
	await expect(main.getByText('of 65,534 used')).toBeVisible();
	await page.goto('/networking?tab=security');
	await expect(main.getByText('deny-lateral').first()).toBeVisible();

	await page.goto('/storage');
	await expect(main.getByText('lvms-vgslow').first()).toBeVisible();
	await page.goto('/storage/bulk-hdd');
	await expect(main.getByText('nfs.csi.k8s.io').first()).toBeVisible();

	await page.goto('/catalog?kind=images');
	await expect(main.getByText('windows-2025').first()).toBeVisible();

	await page.goto('/changes');
	await expect(main.getByText('web-1').first()).toBeVisible();
});

test('one VM among thousands: detail, its staged disk move, its snapshots', async ({ page }) => {
	const main = page.locator('main');
	await page.goto('/vm/web-prod/web-1');
	await expect(page.getByRole('button', { name: /Edit Settings/ })).toBeVisible();
	await expect(main.getByText('host-07').first()).toBeVisible();
	await expect(page.getByRole('button', { name: '1 staged, not proposed' })).toBeVisible();
	await page.goto('/vm/web-prod/web-1?tab=snapshots');
	await expect(main.getByText('before-release-41')).toBeVisible();
});

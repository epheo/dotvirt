import { expect, login, setScenario, test } from './fx';

// The Storage section: the Library's counts drill into the Disks tab, a bulk
// move stages one edit per VM into each project's draft, one disk opens beside
// the table, and the rail scopes every tab to a project.

test.beforeEach(async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
});

test('library counts drill into the disks tab, pre-filtered', async ({ page }) => {
	await page.locator('aside').getByRole('link', { name: 'Storage', exact: true }).click();
	const main = page.locator('main');
	await expect(main.getByRole('heading', { name: 'Storage', level: 1 })).toBeVisible();
	await expect(main.getByText('6 disks on 2 classes, 3 hosts')).toBeVisible();
	// fedora-42 made two disks, centos-stream-9 one; debian-13 made none, so it
	// folds into the rest instead of leading.
	const images = main.getByRole('table');
	await expect(images.getByRole('row').nth(1)).toContainText('fedora-42');
	await expect(images.getByRole('row').nth(1)).toContainText(
		'Templates fedora-server, fedora-minimal',
	);
	await expect(images).not.toContainText('debian-13');

	await main.getByRole('link', { name: 'Show the 5 disks' }).click();
	await expect(page).toHaveURL(/tab=disks&f=nolm/);
	await expect(
		main.getByRole('button', { name: 'Remove filter: Cannot live-migrate' }),
	).toBeVisible();
	await expect(main.getByText('5 of 6 disks')).toBeVisible();
	await expect(main.getByText('Showing 1-5 of 5')).toBeVisible();
});

test('a bulk move stages one edit per running VM, into each project draft', async ({ page }) => {
	await page.goto('/storage?tab=disks&f=nolm');
	const main = page.locator('main');
	await main.getByRole('checkbox', { name: 'Select all 5 on this page' }).check();
	await expect(main.getByText('5 selected')).toBeVisible();
	// web-2 is stopped: its disks would copy from nothing, so they stay out.
	await expect(
		main.getByText('2 disks of stopped VMs are left out: a disk copies only while its VM runs.'),
	).toBeVisible();
	await expect(main.getByText('Adds 3 moves to 3 project drafts.')).toBeVisible();
	await main.getByRole('button', { name: 'Move 3 disks to bulk-hdd' }).click();
	await expect(page.getByText('Moves to bulk-hdd staged for 3 of 3.')).toBeVisible();

	const draft = await (await page.request.get('/api/draft?project=team-db')).json();
	expect(draft.items).toEqual([
		expect.objectContaining({
			name: 'db-1',
			edit: { migrateVolumes: [{ name: 'data', storageClass: 'bulk-hdd' }] },
		}),
	]);
	const row = main.locator('tbody tr', { hasText: 'web-1' });
	await expect(row.getByText('staged')).toBeVisible();
});

test('one disk opens beside the table and snapshots now', async ({ page }) => {
	await page.goto('/storage?tab=disks');
	const main = page.locator('main');
	await main.locator('tbody tr', { hasText: 'web-1' }).click();
	const panel = main.getByRole('complementary', { name: 'web-1 rootdisk' });
	await expect(panel.getByRole('link', { name: 'fedora-42' })).toBeVisible();
	await expect(panel.getByText('Pinned to worker-1.')).toBeVisible();
	await expect(panel.getByRole('link', { name: '12 snapshots' })).toBeVisible();
	// Restore needs the VM stopped, and stopping is a pull request.
	await expect(panel.getByRole('button', { name: 'Restore to before-release-41' })).toBeDisabled();
	await expect(panel.getByRole('button', { name: 'Stage power off' })).toBeVisible();

	await panel.getByRole('button', { name: 'Snapshot now' }).click();
	await expect(panel.getByRole('link', { name: '13 snapshots' })).toBeVisible();
	await panel.getByRole('button', { name: 'Close web-1 rootdisk' }).click();
	await expect(panel).toBeHidden();
});

test('the rail scopes every tab to a project', async ({ page }) => {
	await page.goto('/storage?tab=disks');
	await page
		.locator('aside')
		.getByRole('link', { name: /team-db/ })
		.click();
	await expect(page).toHaveURL(/project=team-db/);
	const main = page.locator('main');
	await expect(main.getByText('team-db: 2 disks on 2 classes')).toBeVisible();
	await expect(main.getByText('2 of 2 disks')).toBeVisible();
	await main.getByRole('button', { name: 'Remove filter: project team-db' }).click();
	await expect(main.getByText('6 of 6 disks')).toBeVisible();
});

test('create VM and add a boot image start from the library', async ({ page }) => {
	await page.goto('/storage');
	const main = page.locator('main');
	await main.getByRole('button', { name: 'Create VM from centos-stream-9' }).click();
	await expect(page.getByRole('dialog')).toBeVisible();
	await page.keyboard.press('Escape');
	await expect(page.getByRole('dialog')).toBeHidden();

	await main.getByRole('button', { name: 'Choose file' }).click();
	const dialog = page.getByRole('dialog', { name: 'Add a boot image' });
	await dialog.locator('input[type=file]').setInputFiles({
		name: 'rhel-10.qcow2',
		mimeType: 'application/octet-stream',
		buffer: Buffer.from('QFI\xfb'),
	});
	await dialog.getByRole('button', { name: 'Upload' }).click();
	await expect(dialog.getByText('is a boot image in')).toBeVisible({ timeout: 15_000 });
});

test('a failed snapshot read says so and offers the retry', async ({ page }) => {
	await setScenario(page, 'degraded');
	await page.goto('/storage');
	const main = page.locator('main');
	await expect(main.getByText('Snapshots unavailable')).toBeVisible();
	await expect(main.getByRole('button', { name: 'Try again' })).toBeVisible();
	await page.goto('/storage?tab=snapshots');
	await expect(main.getByText('cluster read failed')).toBeVisible();
});

test('the search finds disks from any tab', async ({ page }) => {
	await page.goto('/storage');
	await page.getByPlaceholder('Find a disk, VM or image').fill('rhel');
	await expect(page).toHaveURL(/tab=disks/);
	await expect(page).toHaveURL(/q=rhel/);
	const main = page.locator('main');
	await expect(main.getByText('1 of 6 disks')).toBeVisible();
	await expect(main.locator('tbody tr')).toHaveCount(1);
	await expect(main.locator('tbody tr')).toContainText('batch-1');
});

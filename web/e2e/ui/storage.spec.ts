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

	const all = await (await page.request.get('/api/drafts')).json();
	const draft = all.drafts.find((d: { project: string }) => d.project === 'team-db').draft;
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
	await main.getByRole('button', { name: 'Open web-1 rootdisk' }).press('Enter');
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
	// The header speaks for the platform; the project narrows the list.
	await expect(main.getByText('6 disks on 2 classes, 3 hosts')).toBeVisible();
	await expect(main.getByText('2 of 6 disks')).toBeVisible();
	await main.getByRole('link', { name: /^Library/ }).click();
	await expect(main.getByText('Showing team-db only.')).toBeVisible();
	await expect(main.getByText('disk cannot live-migrate', { exact: true })).toBeVisible();
	await main.getByRole('link', { name: 'Show all projects' }).click();
	await expect(page).not.toHaveURL(/project=/);
	await page.goto('/storage?tab=disks&project=team-db');
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
	const dialog = page.getByRole('dialog', { name: 'New image' });
	await dialog.locator('input[type=file]').setInputFiles({
		name: 'rhel-10.qcow2',
		mimeType: 'application/octet-stream',
		buffer: Buffer.from('QFI\xfb'),
	});
	await dialog.getByRole('button', { name: 'Upload image' }).click();
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

test('at scale the disks tab reads like the board and pages 50 at a time', async ({ page }) => {
	test.setTimeout(60_000);
	await setScenario(page, 'scale');
	await page.goto('/storage');
	const main = page.locator('main');
	await expect(main.getByText('3,412 disks on 3 classes, 48 hosts')).toBeVisible();
	await expect(main.getByText('2,980', { exact: true })).toBeVisible();
	await expect(main.getByText('611', { exact: true })).toBeVisible();
	await expect(main.getByText('41 without, 43% of them')).toBeVisible();

	await page.goto('/storage?tab=disks&f=nolm&project=team-web');
	await expect(main.getByText('389 of 3,412 disks')).toBeVisible();
	await expect(main.getByText('Page 1 of 8')).toBeVisible();
	await main.getByRole('button', { name: 'Next page' }).click();
	await expect(main.getByText('Page 2 of 8')).toBeVisible();
	await expect(page).toHaveURL(/page=2/);
	await main.getByLabel('Rows per page').selectOption('100');
	await expect(main.getByText('Page 1 of 4')).toBeVisible();
	await expect(page).toHaveURL(/per=100/);
	await expect(page).not.toHaveURL(/page=/);

	// web-1's move is already staged in the team-web draft.
	await main.getByRole('checkbox', { name: 'Select all 100 on this page' }).check();
	await expect(main.getByText(/web-1 rootdisk is already in it\./)).toBeVisible();
});

test('images nothing was made from fold into one plain row', async ({ page }) => {
	await page.goto('/storage');
	const row = page.locator('main tbody tr', { hasText: 'more images' });
	await expect(row).toContainText('2 more images, none used by a disk yet');
	await expect(row.getByRole('img')).toHaveCount(0);
	await expect(row.getByRole('link', { name: 'Show all 2' })).toBeVisible();
});

test('a VM name in two namespaces shows the namespace on its rows', async ({ page }) => {
	await setScenario(page, 'dupnames');
	await page.goto('/storage?tab=disks&q=test');
	const main = page.locator('main');
	await expect(main.locator('tbody tr')).toHaveCount(2);
	await expect(main.locator('tbody tr').nth(0)).toContainText(/test\s*qa-a/);
	await expect(main.locator('tbody tr').nth(1)).toContainText(/test\s*qa-b/);
	// A name that appears once reads bare.
	await page.goto('/storage?tab=disks&q=web-1');
	await expect(main.locator('tbody tr')).toHaveCount(1);
	await expect(main.locator('tbody tr')).not.toContainText('web-prod');

	await page.goto('/storage?tab=snapshots');
	await expect(main.locator('tbody tr', { hasText: 'test-before-upgrade' })).toContainText('qa-a');
	await expect(main.locator('tbody tr', { hasText: 'test-nightly' })).toContainText('qa-b');
	await expect(main.locator('tbody tr', { hasText: 'before-release-41' })).not.toContainText(
		'web-prod',
	);
});

test('the drop zone offers only what the caller may do', async ({ page }) => {
	const main = page.locator('main');
	// May upload, may not add boot images: a plain upload, no boot-image choice.
	await setScenario(page, 'noboot');
	await page.goto('/storage');
	await expect(main.getByText('Upload a disk image.')).toBeVisible();
	await main.getByRole('button', { name: 'Choose file' }).click();
	const dialog = page.getByRole('dialog', { name: 'New image' });
	await expect(dialog.getByText('Disk size')).toBeVisible();
	await expect(dialog.getByText('Pick an image file')).toBeVisible();
	await expect(dialog.getByText('Offer it as a boot image')).toHaveCount(0);
	await page.keyboard.press('Escape');

	// May do neither: no drop zone, one quiet line saying why.
	await setScenario(page, 'noupload');
	await page.goto('/storage');
	await expect(main.getByText('your account has it in none')).toBeVisible();
	await expect(main.getByRole('button', { name: 'Choose file' })).toHaveCount(0);

	// No upload proxy known: the line names the cluster's setting, not the account.
	await setScenario(page, 'uploadoff');
	await page.goto('/storage');
	await expect(main.getByText(/Image upload is off/)).toBeVisible();
	await expect(main.getByText('your account has it in none')).toHaveCount(0);
	await expect(main.getByRole('button', { name: 'Choose file' })).toHaveCount(0);

	// May add boot images: the choice shows, on by default from the drop zone.
	await setScenario(page, 'base');
	await page.goto('/storage');
	await main.getByRole('button', { name: 'Choose file' }).click();
	await expect(
		page
			.getByRole('dialog', { name: 'New image' })
			.getByLabel('Offer it as a boot image', { exact: false }),
	).toBeChecked();
});

test('imported disks have their own library row and filter, shown without the query', async ({
	page,
}) => {
	await page.goto('/storage');
	const main = page.locator('main');
	const imported = main.locator('tbody tr', { hasText: 'Imported from a registry or URL' });
	await expect(imported).toContainText('From downloads.example.com/seed/pg-seed-2026.qcow2');
	await expect(main.locator('tbody tr', { hasText: 'Not from an image' })).toContainText(
		'Blank or cloned disks',
	);
	await imported.getByRole('link', { name: 'Show disks imported from a registry or URL' }).click();
	await expect(page).toHaveURL(/tab=disks&source=any/);
	await expect(
		main.getByRole('button', { name: 'Remove filter: imported from any' }),
	).toBeVisible();
	await expect(main.getByText('1 of 6 disks')).toBeVisible();
	const row = main.locator('tbody tr');
	await expect(row).toHaveCount(1);
	await expect(row).toContainText(/URL\s*pg-seed-2026\.qcow2/);

	await row.getByRole('button', { name: 'Open db-1 data' }).click();
	const panel = main.getByRole('complementary', { name: 'db-1 data' });
	await expect(panel).toContainText(
		'Imported from https://downloads.example.com/seed/pg-seed-2026.qcow2',
	);
	await expect(panel.getByText('Shown without the query string')).toBeVisible();
	await panel.getByRole('link', { name: 'From a URL' }).click();
	await expect(page).toHaveURL(/source=url%3Ahttps/);
	await expect(main.getByText('1 of 6 disks')).toBeVisible();
	expect(await page.locator('body').innerText()).not.toMatch(/qcow2\?|X-Amz/);
});

test('a source imported into several disks offers to become a boot image, prefilled', async ({
	page,
}) => {
	test.setTimeout(60_000);
	await setScenario(page, 'scale');
	await page.goto('/storage');
	const main = page.locator('main');
	await expect(main.getByText('Imported from a registry or URL')).toBeVisible();
	await expect(main.getByText('From 4 sources')).toBeVisible();
	await expect(
		main.getByText(/6 disks each downloaded\s*quay\.io\/containerdisks\/fedora:42\./),
	).toBeVisible();
	await main.getByRole('button', { name: 'Make it a boot image' }).click();

	const dialog = page.getByRole('dialog', { name: 'New image' });
	await expect(
		dialog.getByText('Filled in from quay.io/containerdisks/fedora:42, which 6 disks downloaded.'),
	).toBeVisible();
	await expect(dialog.getByRole('radio', { name: /^Registry / })).toBeChecked();
	await expect(dialog.getByRole('textbox', { name: 'Image reference' })).toHaveValue(
		'quay.io/containerdisks/fedora:42',
	);
	await expect(dialog.getByRole('textbox', { name: 'Name', exact: true })).toHaveValue('fedora-42');
	await expect(dialog.getByLabel('Disk size')).toHaveValue('30Gi');
	await expect(dialog.getByText('The cluster downloads it once; new VMs clone it.')).toBeVisible();
	await dialog.getByRole('button', { name: 'Add boot image' }).click();
	await expect(dialog.getByText('is a boot image in')).toBeVisible({ timeout: 20_000 });
	await dialog.getByRole('button', { name: 'Done' }).click();

	// That source has its boot image now: the offer moves to the next one.
	await expect(
		main.getByText(/3 disks each downloaded\s*quay\.io\/containerdisks\/centos-stream:9\./),
	).toBeVisible();
});

test('a boot image from a URL: checked as typed, then downloaded by the cluster', async ({
	page,
}) => {
	await page.goto('/storage');
	const main = page.locator('main');
	await main.getByRole('button', { name: 'add one from a URL or registry' }).click();
	const dialog = page.getByRole('dialog', { name: 'New image' });
	await expect(dialog.getByRole('radio', { name: /^URL / })).toBeChecked();
	await expect(dialog.getByText('without credentials')).toBeVisible();
	await dialog.locator('#img-address').fill('ftp://cloud-images.example.org/noble.img');
	await expect(
		dialog.getByRole('status').filter({ hasText: 'The URL needs http:// or https:// and a host' }),
	).toBeVisible();
	await expect(dialog.getByRole('button', { name: 'Add boot image' })).toBeDisabled();

	await dialog
		.locator('#img-address')
		.fill('https://cloud-images.example.org/noble/noble-server-cloudimg-amd64.img');
	await expect(dialog.getByRole('textbox', { name: 'Name', exact: true })).toHaveValue(
		'noble-server-cloudimg-amd64',
	);
	await dialog.getByRole('button', { name: 'Add boot image' }).click();
	await expect(dialog.getByText('Downloading, 42.0%')).toBeVisible({ timeout: 10_000 });
	// The cluster downloads on its own: closing loses nothing, so nothing pins the sheet.
	await expect(
		dialog.getByText('The cluster is downloading it. You can close this.'),
	).toBeVisible();
	await expect(dialog.getByLabel('Close')).toBeEnabled();
	await expect(dialog.getByText('is a boot image in')).toBeVisible({ timeout: 15_000 });
	await expect(dialog.getByRole('button', { name: 'New VM from it' })).toBeVisible();
});

test('a download the cluster cannot reach says why and what next', async ({ page }) => {
	await page.goto('/storage');
	await page
		.locator('main')
		.getByRole('button', { name: 'add one from a URL or registry' })
		.click();
	const dialog = page.getByRole('dialog', { name: 'New image' });
	await dialog.locator('#img-address').fill('https://unreachable.example/a.img?sig=s3cret');
	await dialog.getByRole('button', { name: 'Add boot image' }).click();
	await expect(dialog.getByText(/The cluster cannot download it: Unable to connect/)).toBeVisible({
		timeout: 10_000,
	});
	await expect(
		dialog.getByText(/If the address is wrong, delete it and add it again/),
	).toBeVisible();
	await expect(dialog).not.toContainText('s3cret');
});

test('a download the cluster cannot reach is deleted, then added again as typed', async ({
	page,
}) => {
	await page.goto('/storage');
	await page
		.locator('main')
		.getByRole('button', { name: 'add one from a URL or registry' })
		.click();
	const dialog = page.getByRole('dialog', { name: 'New image' });
	await dialog.locator('#img-address').fill('https://unreachable.example/noble.img');
	await dialog.getByRole('button', { name: 'Add boot image' }).click();
	await expect(dialog.getByText(/The cluster cannot download it/)).toBeVisible({ timeout: 10_000 });
	await expect(
		dialog.getByText('Removes noble and its disk. It never finished, so no VM was made from it.'),
	).toBeVisible();
	await dialog.getByRole('button', { name: 'Delete it' }).click();
	await dialog.getByRole('button', { name: 'Confirm delete' }).click();

	await expect(
		dialog.getByText(/Deleted\s*noble\s*and its disk\.\s*Fix what was wrong/),
	).toBeVisible();
	await expect(dialog.locator('#img-address')).toHaveValue('https://unreachable.example/noble.img');
	await expect(dialog.getByRole('textbox', { name: 'Name', exact: true })).toHaveValue('noble');
	await dialog.locator('#img-address').fill('https://images.example.org/noble.img');
	await expect(dialog.getByRole('button', { name: 'Add boot image' })).toBeEnabled();
	await dialog.getByRole('button', { name: 'Add boot image' }).click();
	await expect(dialog.getByText('is a boot image in')).toBeVisible({ timeout: 15_000 });
});

test('a download that fails for good offers the delete beside the failure', async ({ page }) => {
	await page.goto('/storage');
	await page
		.locator('main')
		.getByRole('button', { name: 'add one from a URL or registry' })
		.click();
	const dialog = page.getByRole('dialog', { name: 'New image' });
	await dialog.locator('#img-address').fill('https://images.example.org/corrupt.img');
	await dialog.getByRole('button', { name: 'Add boot image' }).click();
	const failed = dialog.getByRole('region', { name: 'What failed' });
	await expect(failed).toContainText('The cluster reported the download failed', {
		timeout: 15_000,
	});
	await expect(failed).toContainText(/corrupt\s*stays in/);
	// Its name is taken until it goes: the retry says so instead of failing.
	await expect(dialog.getByRole('button', { name: 'Try again' })).toBeDisabled();
	await expect(dialog.getByRole('status')).toContainText('corrupt is already a boot image');
	await failed.getByRole('button', { name: 'Delete it' }).click();
	await failed.getByRole('button', { name: 'Confirm delete' }).click();
	await expect(dialog.getByText(/Deleted\s*corrupt\s*and its disk/)).toBeVisible();
	await expect(dialog.getByRole('button', { name: 'Add boot image' })).toBeEnabled();
});

test('the images tab says where an unfinished boot image stands and deletes it', async ({
	page,
}) => {
	await setScenario(page, 'unfinished');
	await page.goto('/storage?tab=images');
	const main = page.locator('main');
	await expect(main.locator('tbody tr', { hasText: 'noble-nightly' })).toContainText('Not ready');

	// Downloading: its progress, and a delete that cancels it.
	await main.getByRole('button', { name: 'fedora-43-beta', exact: true }).click();
	let panel = main.getByRole('complementary', { name: 'fedora-43-beta' });
	await expect(panel).toContainText('Downloading, 63.5%');
	await expect(panel).toContainText('Stops the download and removes fedora-43-beta and its disk.');
	await expect(panel.getByRole('button', { name: 'Delete' })).toBeEnabled();

	// Failed: the reason, then the delete.
	await main.getByRole('button', { name: 'noble-nightly', exact: true }).click();
	panel = main.getByRole('complementary', { name: 'noble-nightly' });
	await expect(panel).toContainText('Failed: Unable to connect to http data source');
	await expect(panel).toContainText(
		'Removes noble-nightly and its disk. It never finished, so no VM was made from it.',
	);
	await panel.getByRole('button', { name: 'Delete' }).click();
	await panel.getByRole('button', { name: 'Confirm delete' }).click();
	await expect(page.getByText('Boot image noble-nightly deleted.')).toBeVisible();
	await expect(main.getByRole('button', { name: 'noble-nightly', exact: true })).toHaveCount(0);
	await expect(page).not.toHaveURL(/image=/);
});

test('a boot image delete the caller may not do, or one never offered', async ({ page }) => {
	await setScenario(page, 'nodelete');
	await page.goto('/storage?tab=images&image=batch-prod%2Fnoble-nightly');
	const main = page.locator('main');
	let panel = main.getByRole('complementary', { name: 'noble-nightly' });
	await expect(panel).toContainText('Failed: Unable to connect');
	await expect(panel.getByRole('button', { name: 'Delete' })).toBeDisabled();
	await expect(panel).toContainText('You may not delete boot images in batch-prod.');

	// Ready: nothing to delete here. Not ready, added outside dotvirt: said so.
	await setScenario(page, 'unfinished');
	await page.goto('/storage?tab=images&image=dotvirt-images%2Ffedora-42');
	panel = main.getByRole('complementary', { name: 'fedora-42' });
	await expect(panel.getByRole('button', { name: 'Create VM' })).toBeEnabled();
	await expect(panel.getByRole('button', { name: 'Delete' })).toHaveCount(0);
	await page.goto('/storage?tab=images&image=dotvirt-images%2Fdebian-13');
	panel = main.getByRole('complementary', { name: 'debian-13' });
	await expect(panel).toContainText('Added outside dotvirt, so it is not deleted here.');
	await expect(panel.getByRole('button', { name: 'Delete' })).toHaveCount(0);
});

test('with upload off, a boot image still comes from a URL or a registry', async ({ page }) => {
	await setScenario(page, 'uploadoff');
	await page.goto('/storage');
	const main = page.locator('main');
	await expect(main.getByText(/Image upload is off/)).toBeVisible();
	await expect(main.getByText(/A URL or a registry needs none\./)).toBeVisible();
	await expect(main.getByRole('button', { name: 'Choose file' })).toHaveCount(0);
	await main.getByRole('button', { name: 'From a URL or registry' }).click();
	const dialog = page.getByRole('dialog', { name: 'New image' });
	await expect(dialog.getByRole('radio', { name: /^Upload a file/ })).toBeDisabled();
	await expect(dialog.getByText(/Image upload is off/)).toBeVisible();
	await dialog.getByRole('radio', { name: /^Registry / }).check();
	await dialog.locator('#img-address').fill('docker://quay.io/containerdisks/fedora:42');
	await expect(dialog.getByRole('textbox', { name: 'Name', exact: true })).toHaveValue('fedora-42');
	await expect(dialog.getByRole('button', { name: 'Add boot image' })).toBeEnabled();
});

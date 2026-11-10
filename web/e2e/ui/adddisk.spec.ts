import { expect, login, openVM, setScenario, stageChange, test } from './fx';
import type { Page } from '@playwright/test';

// Add disk on r-drbd: protection chosen as outcomes, one card per copy count,
// each landing on a class the caller may list. Anywhere else, the class list.

const ASK = /: how much should it survive\?$/;

async function diskEditor(page: Page, scenario: string, before?: (p: Page) => Promise<unknown>) {
	await setScenario(page, scenario);
	await before?.(page);
	await login(page);
	await page.goto('/compute/team-web/web-prod');
	await page.getByTitle('Create a VM, network, rule or project').click();
	const sheet = page.getByRole('dialog');
	await sheet.getByRole('button', { name: 'Change disk' }).click();
	return sheet;
}

test('an operator picks how much a new VM disk survives, with the room left', async ({ page }) => {
	const sheet = await diskEditor(page, 'replicated');
	await expect(sheet.getByText('Root disk: how much should it survive?')).toBeVisible();
	await expect(sheet.getByRole('radio', { name: /^Three copies/ })).toBeDisabled();
	await expect(
		sheet.getByText('No class you can use keeps 3 copies. Ask your platform team to add one.'),
	).toBeVisible();

	await sheet.getByRole('radio', { name: /^Two copies/ }).check();
	await sheet.getByRole('radio', { name: /^r-drbd-replicated/ }).check();
	await expect(sheet.getByText('Class r-drbd-replicated, tier nvme.')).toBeVisible();
	await expect(sheet.getByText('Fits. Room on nvme: 810 GiB at 2 copies.')).toBeVisible();

	const posted = page.waitForRequest(
		(r) =>
			r.method() === 'POST' &&
			new URL(r.url()).pathname === '/api/vms' &&
			!r.url().includes('dryRun'),
	);
	await sheet.getByRole('button', { name: 'Done' }).click();
	await sheet.getByRole('button', { name: 'Stage VM' }).click();
	expect((await posted).postDataJSON()).toMatchObject({ storageClass: 'r-drbd-replicated' });
});

test('a tenant gets the same cards and no room it could not read', async ({ page }) => {
	const sheet = await diskEditor(page, 'replicatedtenant');
	const pick = sheet.getByRole('group', { name: 'Root disk: how much should it survive?' });
	await pick.getByRole('radio', { name: /^One copy/ }).check();
	await expect(pick.getByText('Class r-drbd-local, tier nvme.')).toBeVisible();
	await expect(pick.getByText(/Room on/)).toHaveCount(0);
	await expect(pick.getByText(/worker-/)).toHaveCount(0);
});

const keeps: [string, string, ((p: Page) => Promise<unknown>) | undefined][] = [
	['without r-drbd', 'base', undefined],
	[
		'while the module read is unanswered',
		'replicated',
		(p) => p.route('**/api/modules', (r) => r.abort()),
	],
	// No list on storageclasses: the server answers an empty list.
	[
		'for a caller who may not list classes',
		'replicated',
		(p) => p.route('**/api/storage/classes', (r) => r.fulfill({ json: [] })),
	],
];
for (const [what, scenario, before] of keeps)
	test(`${what} Add disk keeps the class list`, async ({ page }) => {
		const sheet = await diskEditor(page, scenario, before);
		await expect(sheet.getByText('Root disk class', { exact: true })).toBeVisible();
		await expect(sheet.getByText(ASK)).toHaveCount(0);
	});

test('a disk added to a VM picks its protection and stages through the edit', async ({ page }) => {
	await setScenario(page, 'replicated');
	await login(page);
	await openVM(page, 'web-1');
	await page.getByRole('button', { name: /Edit Settings/ }).click();
	const dialog = page.getByRole('dialog');
	await dialog.getByRole('button', { name: /Storage/ }).click();
	await dialog.getByRole('button', { name: '+ Add hard disk' }).click();
	await expect(dialog.getByText(ASK)).toBeVisible();
	await dialog.getByRole('radio', { name: /^Two copies/ }).check();
	await dialog.getByRole('radio', { name: /^r-drbd-replicated/ }).check();

	const posted = page.waitForRequest(
		(r) => r.method() === 'POST' && new URL(r.url()).pathname.endsWith('/edit'),
	);
	await stageChange(page);
	expect((await posted).postDataJSON().addDisks).toEqual([
		expect.objectContaining({ storageClass: 'r-drbd-replicated' }),
	]);
});

test('without r-drbd a disk added to a VM keeps its class select', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await openVM(page, 'web-1');
	await page.getByRole('button', { name: /Edit Settings/ }).click();
	const dialog = page.getByRole('dialog');
	await dialog.getByRole('button', { name: /Storage/ }).click();
	await dialog.getByRole('button', { name: '+ Add hard disk' }).click();
	await expect(dialog.getByRole('combobox')).toHaveCount(1);
	await expect(dialog.getByText(ASK)).toHaveCount(0);
});

test('one-copy disks of a new VM are weighed together, as they share a host', async ({ page }) => {
	const sheet = await diskEditor(page, 'replicated');
	await sheet.locator('#vm-disk').fill('600Gi');
	await sheet.getByRole('radio', { name: /^One copy/ }).check();
	await expect(sheet.getByText('Fits. Room on nvme: 1.2 TiB at one copy.')).toBeVisible();

	await sheet.getByRole('button', { name: 'Add a disk' }).click();
	await sheet.getByLabel('Disk 2 size').fill('600Gi');
	await sheet
		.getByRole('group', { name: ASK })
		.nth(1)
		.getByRole('radio', { name: /^One copy/ })
		.check();
	// worker-3 takes both, but the scheduler may pick worker-1, which takes each alone.
	await expect(
		sheet.getByText("May not fit on one host with the VM's other one-copy disks."),
	).toHaveCount(2);
});

test("a disk added to a VM with a one-copy disk is judged on that disk's host", async ({
	page,
}) => {
	await setScenario(page, 'replicated');
	await login(page);
	await openVM(page, 'web-2');
	await page.getByRole('button', { name: /Edit Settings/ }).click();
	const dialog = page.getByRole('dialog');
	await dialog.getByRole('button', { name: /Storage/ }).click();
	await dialog.getByRole('button', { name: '+ Add hard disk' }).click();
	await dialog.getByLabel('New disk size').fill('700Gi');
	await dialog.getByRole('radio', { name: /^One copy/ }).check();
	await expect(
		dialog.getByText(
			"Fits on worker-3, where the VM's one-copy disk lives. Room on nvme there: 1.2 TiB.",
		),
	).toBeVisible();

	await dialog.getByRole('button', { name: '+ Add hard disk' }).click();
	await dialog.getByLabel('New disk size').nth(1).fill('700Gi');
	await dialog
		.getByRole('group', { name: ASK })
		.nth(1)
		.getByRole('radio', { name: /^One copy/ })
		.check();
	await expect(
		dialog.getByText(
			'Too big for worker-3 with the other new one-copy disks. Room on nvme there: 1.2 TiB.',
		),
	).toHaveCount(2);
});

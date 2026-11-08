import { expect, login, setScenario, test } from './fx';

// The UI never offers what the server would refuse: each control reads the
// capability field its route asks, and a refused one hides or names the
// refusal in one line. The caps scenario says no to a handful of fields.

test.describe('refused fields', () => {
	test.beforeEach(async ({ page }) => {
		await setScenario(page, 'caps');
		await login(page);
	});

	test('VM verbs the server refuses are disabled and say why', async ({ page }) => {
		await page.goto('/vm/web-prod/web-1');
		const restart = page.getByRole('button', { name: 'Restart', exact: true });
		await expect(restart).toBeDisabled();
		await expect(restart).toHaveAttribute('title', 'You may not restart VMs in web-prod.');
		await expect(page.getByRole('button', { name: 'Console', exact: true })).toHaveAttribute(
			'title',
			'You may not open VM consoles in web-prod.',
		);
		// Changing the VM is still allowed: the refusals are per field.
		await expect(page.getByRole('button', { name: /Edit Settings/ })).toBeEnabled();
		await page.getByRole('button', { name: 'Actions' }).click();
		const del = page.getByRole('button', { name: 'Delete VM' });
		await expect(del).toBeDisabled();
		await expect(del).toHaveAttribute('title', 'You may not delete VMs in web-prod.');
	});

	test('the console and snapshot tabs name the refusal', async ({ page }) => {
		await page.goto('/vm/web-prod/web-1?tab=console');
		await expect(
			page.locator('main').getByText('You may not open VM consoles in web-prod.'),
		).toBeVisible();
		await page.goto('/vm/web-prod/web-1?tab=snapshots');
		const take = page.getByRole('button', { name: 'Take snapshot' });
		await expect(take).toBeDisabled();
		await expect(take).toHaveAttribute('title', 'You may not take snapshots in web-prod.');
	});

	test('a new VM in a namespace that refuses it waits on that', async ({ page }) => {
		await page.getByRole('button', { name: /^New$/ }).click();
		const sheet = page.getByRole('dialog');
		await expect(sheet.getByRole('status')).toHaveText(/You may not create VMs in /);
		await expect(sheet.getByRole('button', { name: 'Stage VM' })).toBeDisabled();
	});

	test('turning rebalancing off reads its own field', async ({ page }) => {
		await page.goto('/hosts?tab=configure');
		const off = page.getByRole('button', { name: 'Disable...' });
		await expect(off).toBeDisabled();
		await expect(off).toHaveAttribute('title', 'You may not turn off rebalancing.');
		await expect(page.getByRole('button', { name: 'Configure', exact: true })).toBeEnabled();
	});

	test('uplinks offer no create to a caller who may not', async ({ page }) => {
		await page.goto('/networking?tab=uplinks');
		await expect(page.getByText('br-physnet')).toBeVisible();
		await expect(page.getByRole('button', { name: 'New uplink' })).toHaveCount(0);
	});

	test('sync access names the refusal on the Configure tab', async ({ page }) => {
		await page.goto('/compute/team-web?tab=configure');
		const grant = page.getByRole('button', { name: 'Grant sync access' });
		await expect(grant).toBeDisabled();
		await expect(grant).toHaveAttribute('title', 'You may not grant sync access.');
	});
});

test.describe('allowed fields', () => {
	test.beforeEach(async ({ page }) => {
		await setScenario(page, 'base');
		await login(page);
	});

	test('uplinks offer create', async ({ page }) => {
		await page.goto('/networking?tab=uplinks');
		await expect(page.getByRole('button', { name: 'New uplink' })).toBeVisible();
	});

	test('sync access stages into the platform draft', async ({ page }) => {
		await page.goto('/compute/team-web?tab=configure');
		await expect(page.getByText('Pull request').first()).toBeVisible();
		await page.getByRole('button', { name: 'Grant sync access' }).click();
		const dialog = page.getByRole('dialog');
		await expect(dialog.getByText(/grants its sync account admin/)).toBeVisible();
		await dialog.getByRole('button', { name: 'Stage grant' }).click();
		await expect(
			page.getByText('Sync access for team-web staged in the platform draft.'),
		).toBeVisible();
	});

	test('the tree menu offers sync access on a project with a repo', async ({ page }) => {
		await page.goto('/compute');
		await page.locator('aside').getByText('team-web', { exact: true }).click({ button: 'right' });
		await page.getByRole('button', { name: 'Grant sync access...' }).click();
		await expect(page.getByRole('dialog').getByText('Grant sync access to team-web')).toBeVisible();
	});
});

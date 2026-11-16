import { expect, login, setScenario, test } from './fx';

// What the install asks a cluster admin to install: offered only to a caller
// the capability says may install it all, and installed with one click.

test('a cluster admin installs what the install asks for, and the banner clears', async ({
	page,
}) => {
	await setScenario(page, 'adminobjects');
	const calls: string[] = [];
	page.on('request', (r) => {
		if (r.url().includes('/api/admin-objects')) calls.push(r.method());
	});
	await login(page);
	const list = page.getByTestId('admin-objects');
	await expect(list).toBeVisible();
	await expect(list.getByRole('listitem')).toHaveText([
		"Bounds the operator's Secrets",
		"Bounds the operator's Deployments",
		'Enforces dotvirt-operator-secrets',
		'Enforces dotvirt-operator-deployments',
	]);
	await page.getByRole('button', { name: 'Install', exact: true }).click();
	await expect(
		page.getByText('Installed what the install asked a cluster admin for.'),
	).toBeVisible();
	await expect(list).toHaveCount(0);
	expect(calls).toContain('POST');
});

test('a caller who may not install it is neither offered it nor reads it', async ({ page }) => {
	await setScenario(page, 'adminobjectsdenied');
	const calls: string[] = [];
	page.on('request', (r) => {
		if (r.url().includes('/api/admin-objects')) calls.push(r.method());
	});
	await login(page);
	await expect(page.getByRole('main')).toBeVisible();
	await expect(page.getByTestId('admin-objects')).toHaveCount(0);
	await expect(page.getByRole('button', { name: 'Install', exact: true })).toHaveCount(0);
	expect(calls).toEqual([]);
});

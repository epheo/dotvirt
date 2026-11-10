import { expect, login, setScenario, test } from './fx';

// A platform admin turns publishing on per namespace from the Configure tab.
// Each namespace says where it stands: on, offered, refused, or waiting.

test.describe('publishing', () => {
	test('each namespace says where it stands', async ({ page }) => {
		await setScenario(page, 'publishing');
		await login(page);
		await page.goto('/compute?tab=configure');
		await expect(page.getByText('*.batch-prod.apps.example.com')).toBeVisible();
		await expect(page.getByRole('button', { name: 'Turn publishing on' })).toHaveCount(2);
		await expect(page.getByTitle('You may not turn publishing on in db-prod.')).toBeDisabled();
	});

	test('stages the namespace into the platform draft', async ({ page }) => {
		await setScenario(page, 'publishing');
		await login(page);
		await page.goto('/compute/team-web?tab=configure');
		await page.getByRole('button', { name: 'Turn publishing on' }).click();
		const dialog = page.getByRole('dialog');
		await expect(dialog.getByText('Turn publishing on in web-prod')).toBeVisible();
		await expect(dialog.getByText('*.web-prod.apps.example.com').first()).toBeVisible();
		await dialog.getByRole('button', { name: 'Stage publishing' }).click();
		await expect(
			page.getByText('Publishing for web-prod staged in the platform draft.'),
		).toBeVisible();
	});

	test('a namespace with its own VM Network says what it waits on', async ({ page }) => {
		await setScenario(page, 'publishingwaits');
		await login(page);
		await page.goto('/compute/team-db?tab=configure');
		await expect(page.getByText(/its own VM Network waits on a portail update/)).toBeVisible();
		await expect(page.getByRole('button', { name: 'Turn publishing on' })).toHaveCount(0);
	});

	test('is not shown where the install publishes nothing', async ({ page }) => {
		await setScenario(page, 'base');
		await login(page);
		await page.goto('/compute?tab=configure');
		await expect(page.getByText('Pull request').first()).toBeVisible();
		await expect(page.getByRole('button', { name: 'Turn publishing on' })).toHaveCount(0);
	});
});

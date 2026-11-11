import { expect, login, setScenario, test } from './fx';

// A platform admin turns publishing on per project from the Configure tab.
// Each project says where it stands: on, offered, refused, or waiting on a
// portail update or its publish namespace, and which namespaces its Gateway
// does not reach.

test.describe('publishing', () => {
	test('each project says where it stands', async ({ page }) => {
		await setScenario(page, 'publishing');
		await login(page);
		await page.goto('/compute?tab=configure');
		await expect(page.getByText('*.team-batch.apps.example.com')).toBeVisible();
		await expect(page.getByRole('button', { name: 'Turn publishing on' })).toHaveCount(2);
		await expect(
			page.getByTitle('You may not turn publishing on for this project.'),
		).toBeDisabled();
		await expect(page.getByText(/Not reached: web-edge/)).toBeVisible();
	});

	test('stages the project into the platform draft', async ({ page }) => {
		await setScenario(page, 'publishing');
		await login(page);
		await page.goto('/compute/team-web?tab=configure');
		await page.getByRole('button', { name: 'Turn publishing on' }).click();
		const dialog = page.getByRole('dialog');
		await expect(dialog.getByText('Turn publishing on for team-web')).toBeVisible();
		await expect(dialog.getByText('*.team-web.apps.example.com').first()).toBeVisible();
		await expect(dialog.getByText('team-web-publish')).toBeVisible();
		await expect(dialog.getByText(/web-edge has a VM Network of its own/)).toBeVisible();
		await dialog.getByRole('button', { name: 'Stage publishing' }).click();
		await expect(
			page.getByText('Publishing for team-web staged in the platform draft.'),
		).toBeVisible();
	});

	test('a project on a VM Network says what it waits on', async ({ page }) => {
		await setScenario(page, 'publishingwaits');
		await login(page);
		await page.goto('/compute/team-db?tab=configure');
		await expect(
			page.getByText(/a project on a VM Network waits on a portail update/),
		).toBeVisible();
		await expect(page.getByRole('button', { name: 'Turn publishing on' })).toHaveCount(0);
	});

	test('a project without a publish namespace says what it waits on, and creates it', async ({
		page,
	}) => {
		await setScenario(page, 'publishingunhoused');
		await login(page);
		await page.goto('/compute/team-db?tab=configure');
		await expect(page.getByText(/publishing needs the namespace team-db-publish/)).toBeVisible();
		await expect(page.getByRole('button', { name: 'Turn publishing on' })).toHaveCount(0);
		await page.getByRole('button', { name: 'Create team-db-publish' }).click();
		const dialog = page.getByRole('dialog');
		await expect(dialog.getByText(/will hold team-db's Gateway/)).toBeVisible();
		await dialog.getByRole('button', { name: 'Stage namespace' }).click();
		await expect(page.getByText('team-db-publish staged in the platform draft.')).toBeVisible();
	});

	test('a project whose namespaces each run their own network is told so', async ({ page }) => {
		await setScenario(page, 'publishingapart');
		await login(page);
		await page.goto('/compute/team-db?tab=configure');
		await expect(page.getByText(/each namespace has a VM Network of its own/)).toBeVisible();
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

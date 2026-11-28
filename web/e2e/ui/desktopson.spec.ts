import { expect, login, setScenario, test } from './fx';

// A platform admin turns desktops on per project namespace from the Configure
// tab, where publishing is. Each namespace says where it stands: on, offered,
// refused, or waiting on its admin grant.

test.describe('desktops on', () => {
	test('each namespace says where it stands', async ({ page }) => {
		await setScenario(page, 'desktopsgrants');
		await login(page);
		await page.goto('/compute?tab=configure');
		await expect(page.getByRole('button', { name: 'Turn desktops on' })).toHaveCount(2);
		await expect(page.getByTitle('You may not turn desktops on in db-prod.')).toBeDisabled();
		await expect(page.getByText('On', { exact: true })).toBeVisible();
	});

	test('stages the namespace into the platform draft', async ({ page }) => {
		await setScenario(page, 'desktopsgrants');
		await login(page);
		await page.goto('/compute/team-web?tab=configure');
		await page.getByRole('button', { name: 'Turn desktops on' }).click();
		const dialog = page.getByRole('dialog');
		await expect(dialog.getByText('Turn desktops on in web-prod')).toBeVisible();
		await expect(dialog.getByText(/Its viewers may not/)).toBeVisible();
		await dialog.getByRole('button', { name: 'Stage desktops' }).click();
		await expect(
			page.getByText('Desktops in web-prod staged in the platform draft.'),
		).toBeVisible();
	});

	test('a namespace without an admin grant says what it waits on', async ({ page }) => {
		await setScenario(page, 'desktopsungranted');
		await login(page);
		await page.goto('/compute/team-web?tab=configure');
		await expect(page.getByText(/desktops follow its admin grant/)).toBeVisible();
		await expect(page.getByRole('button', { name: 'Turn desktops on' })).toHaveCount(0);
	});

	test('is not shown where the cluster serves no DeskuBe', async ({ page }) => {
		await setScenario(page, 'base');
		await login(page);
		await page.goto('/compute?tab=configure');
		await expect(page.getByText('Pull request').first()).toBeVisible();
		await expect(page.getByRole('button', { name: 'Turn desktops on' })).toHaveCount(0);
	});
});

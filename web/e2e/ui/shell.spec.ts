import { expect, login, setScenario, test } from './fx';

// Shell + inventory smoke against the base scenario, plus the empty-cluster
// onboarding state — hermetic versions of the live-stack smoke suite.

test('base: shell, tree and VM table render from the stream', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);

	const aside = page.locator('aside');
	await expect(aside.getByText('team-web')).toBeVisible();
	await expect(aside.getByText('team-db')).toBeVisible();

	const main = page.locator('main');
	await main.getByRole('link', { name: 'VMs', exact: true }).click();
	await expect(main.getByText('web-1')).toBeVisible();
	await expect(main.getByText('web-2')).toBeVisible();

	// The stopped VM reads as stopped, not as an error state.
	const stopped = main.locator('tbody tr', { hasText: 'web-2' });
	await expect(stopped).toContainText(/Stopped|Off/);

	// No standing problems in a healthy fleet: the issues bell is clean.
	await page.locator('button[title^="Issues"]').click();
	await expect(page.getByText('No standing issues.')).toBeVisible();
});

test('base: VM detail opens with live facts and tabs', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.locator('main').getByRole('link', { name: 'VMs', exact: true }).click();
	await page
		.locator('tbody tr', { hasText: 'web-1' })
		.first()
		.getByRole('link', { name: 'web-1', exact: true })
		.click();

	await expect(page).toHaveURL(/\/vm\/web-prod\/web-1/);
	await expect(page.getByRole('button', { name: /Edit Settings/ })).toBeVisible();
	await expect(page.locator('main').getByText('worker-1').first()).toBeVisible();
	await expect(page.locator('main').getByText('10.128.0.10').first()).toBeVisible();
	await expect(page.getByRole('link', { name: 'Snapshots', exact: true })).toBeVisible();
	await expect(page.getByRole('link', { name: 'Console', exact: true })).toBeVisible();
});

test('empty: onboarding CTA instead of a dead-end blank tree', async ({ page }) => {
	await setScenario(page, 'empty');
	await login(page);

	await expect(page.getByText('No projects visible.')).toBeVisible();
	// Guided-user rule: the empty state carries its next action.
	await expect(page.getByRole('button', { name: 'Create your first project' })).toBeVisible();
});

test('deep links survive a hard reload', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/vm/web-prod/web-1');
	await expect(page.getByRole('button', { name: /Edit Settings/ })).toBeVisible();
	await page.reload();
	await expect(page.getByRole('button', { name: /Edit Settings/ })).toBeVisible();
	await page.goto('/catalog?kind=instancetypes');
	await expect(page.getByText('Read-only — these are platform objects')).toBeVisible();
});

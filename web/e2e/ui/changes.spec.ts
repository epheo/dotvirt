import { expect, login, setScenario, test } from './fx';

// The /changes review route on its own: PR review state from the forge, the
// merge-in-forge doctrine, and deep-linkability.

test('the PR lane shows checks and approval state, merge stays in the forge', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/changes');

	const main = page.locator('main');
	const laneRow = main.getByRole('button', { name: /PR #41/ });
	await expect(laneRow).toBeVisible();
	await expect(main.getByText('Checks passed').first()).toBeVisible();
	await expect(main.getByText('Awaiting 1 approval').first()).toBeVisible();

	// Selecting the PR offers exactly one action: the forge deep link. No
	// in-app merge button exists.
	await laneRow.click();
	const link = main.getByRole('link', { name: /Open PR to approve and merge/ });
	await expect(link).toBeVisible();
	await expect(link).toHaveAttribute('href', /pulls\/41/);
	await expect(main.getByRole('button', { name: /^Merge/ })).toHaveCount(0);
	await expect(main.getByText(/Approval and merge happen in the forge/)).toBeVisible();
});

test('the route explains the write model when nothing is in flight', async ({ page }) => {
	await setScenario(page, 'empty');
	await login(page);
	await page.goto('/changes');
	await expect(page.locator('main').getByText('Nothing in flight.')).toBeVisible();
});

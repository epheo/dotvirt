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

test('a past change reviews like a staged one and reverts as a new PR', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/changes');

	const main = page.locator('main');
	await main.getByRole('button', { name: /^team-web$/ }).click();
	// A forge merge is named by the PR it merged, not the merge boilerplate.
	const row = main.getByRole('button', { name: /web-2: add data disk/ });
	await expect(row).toBeVisible();
	await row.click();

	await expect(main.getByRole('link', { name: /PR #40/ })).toHaveAttribute('href', /pulls\/40/);
	await expect(main.getByText(/data \(50Gi\)/)).toBeVisible();

	// Revert is two clicks and yields a pull request, never a direct change.
	await main.getByRole('button', { name: 'Revert as pull request' }).click();
	await main.getByRole('button', { name: 'Confirm revert' }).click();
	// The stepper and the note both link the revert PR.
	await expect(main.getByRole('link', { name: /PR #78/ }).first()).toHaveAttribute(
		'href',
		/pulls\/78/,
	);
	await expect(main.getByText(/Approve and merge it in the forge/)).toBeVisible();
});

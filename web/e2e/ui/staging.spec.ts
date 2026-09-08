import { expect, login, openVM, setScenario, stageChange, test } from './fx';

// The staging lifecycle end to end in the UI: edit -> staged draft -> the
// /changes review route -> propose. The highest-blast-radius flow in the
// product, previously covered only against a live stack.

test('edit stages into the draft and proposes as a PR', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await openVM(page, 'web-1');

	// Stage a memory change through Edit Settings.
	await page.getByRole('button', { name: /Edit Settings/ }).click();
	const memory = page.getByLabel('Memory');
	await memory.fill('8Gi');
	await stageChange(page);

	// The Review-changes badge picks the draft up without a reload (1 staged +
	// the base scenario's standing PR #41).
	const review = page.getByRole('link', { name: /Review changes/ });
	await expect(review).toContainText('2');

	// The route shows the semantic item with its impact, not YAML.
	await review.click();
	await expect(page).toHaveURL(/\/changes/);
	const main = page.locator('main');
	await expect(main.getByText('web-prod/web-1')).toBeVisible();
	await expect(main.getByText(/memory/i).first()).toBeVisible();
	await expect(main.getByText(/Restart required/)).toBeVisible();

	// Propose: title required, then the PR lands in the Proposed lane.
	await main.getByPlaceholder('Pull request title').fill('web-1: raise memory to 8Gi');
	await main.getByRole('button', { name: 'Propose pull request' }).click();
	const lane77 = main.getByRole('button', { name: /PR #77/ });
	await expect(lane77).toBeVisible();
	// Merge stays in the forge: the one affordance is the deep link.
	await lane77.click();
	await expect(main.getByRole('link', { name: /Open PR to approve and merge/ })).toBeVisible();
});

test('a failed stage shows the backend detail in the dialog', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await openVM(page, 'web-2');

	// web-2 lives in team-web too — force the failure by proposing with no
	// title, which the fixture (like the backend) refuses with a 400.
	await page.getByRole('button', { name: /Edit Settings/ }).click();
	await page.getByLabel('Memory').fill('16Gi');
	await stageChange(page);
	await page.getByRole('link', { name: /Review changes/ }).click();
	const main = page.locator('main');
	await main.getByRole('button', { name: 'Propose pull request' }).click();
	// The error surfaces inline; the draft is not silently lost.
	await expect(main.getByText(/title is required/i).first()).toBeVisible();
	await expect(main.getByText('web-2').first()).toBeVisible();
});

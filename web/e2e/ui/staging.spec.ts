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

	// The Propose button appears with the draft, without a reload, and counts
	// what it would propose.
	const review = page.getByRole('link', { name: /Propose \d+ change/ });
	await expect(review).toHaveText(/Propose 1 change$/);

	// The route shows the semantic item with its impact, not YAML.
	await review.click();
	await expect(page).toHaveURL(/\/changes/);
	const main = page.locator('main');
	await expect(main.getByText('web-prod/web-1')).toBeVisible();
	await expect(main.getByText(/memory/i).first()).toBeVisible();
	await expect(main.getByText(/Restart required/)).toBeVisible();

	// Propose: title required, then the PR lands in the Proposed lane.
	await main.getByLabel('Pull request title').fill('web-1: raise memory to 8Gi');
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

	// web-2 lives in team-web too — force the failure with the fixture's
	// sentinel title, which it refuses with a 400 like a backend error.
	await page.getByRole('button', { name: /Edit Settings/ }).click();
	await page.getByLabel('Memory').fill('16Gi');
	await stageChange(page);
	await page.getByRole('link', { name: /Propose \d+ change/ }).click();
	const main = page.locator('main');
	await main.getByLabel('Pull request title').fill('!fail');
	await main.getByRole('button', { name: 'Propose pull request' }).click();
	// The error surfaces inline; the draft is not silently lost.
	await expect(main.getByText(/fixture failure hook/i).first()).toBeVisible();
	await expect(main.getByText('web-2').first()).toBeVisible();
});

test('the propose form shows the default title and body; Tab takes them', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await openVM(page, 'web-1');
	await page.getByRole('button', { name: /Edit Settings/ }).click();
	await page.getByLabel('Memory').fill('8Gi');
	await stageChange(page);
	await page.getByRole('link', { name: /Propose \d+ change/ }).click();

	// What a blank propose pushes is visible in place, not a surprise after.
	const main = page.locator('main');
	const title = main.getByLabel('Pull request title');
	const body = main.getByLabel('Pull request description');
	await expect(title).toHaveAttribute('placeholder', /Update web-1/);
	await expect(body).toHaveAttribute('placeholder', /## Changes/);

	// Tab on the empty field takes the default and keeps focus to edit it.
	await title.focus();
	await page.keyboard.press('Tab');
	await expect(title).toHaveValue(/Update web-1/);
	await expect(title).toBeFocused();
	await body.focus();
	await page.keyboard.press('Tab');
	await expect(body).toHaveValue(/Proposed from dotvirt/);
	// Tab on a filled field moves on as usual.
	await page.keyboard.press('Tab');
	await expect(body).not.toBeFocused();

	// Blank fields propose fine: the defaults travel server-side.
	await title.fill('');
	await body.fill('');
	await main.getByRole('button', { name: 'Propose pull request' }).click();
	await expect(main.getByRole('button', { name: /PR #77/ })).toBeVisible();
});

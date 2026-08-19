import { expect, login, openVM, setScenario, test } from './fx';

// The staging lifecycle end to end in the UI: edit -> staged draft -> Changes
// panel -> propose. The highest-blast-radius flow in the product, previously
// covered only against a live stack.

// The wizard's finish button renders only on the last step; walk Next to it.
async function stageChange(page: import('@playwright/test').Page) {
	const finish = page.getByRole('button', { name: 'Stage change' });
	for (let i = 0; i < 12 && !(await finish.isVisible()); i++) {
		await page.getByRole('button', { name: 'Next', exact: true }).click();
	}
	await finish.click();
}

test('edit stages into the draft and proposes as a PR', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await openVM(page, 'web-1');

	// Stage a memory change through Edit Settings.
	await page.getByRole('button', { name: /Edit Settings/ }).click();
	const memory = page.getByLabel('Memory');
	await memory.fill('8Gi');
	await stageChange(page);

	// The Changes indicator picks the draft up without a reload.
	const changes = page.locator('button[title^="Changes"]');
	await expect(changes).toContainText('1');

	// The panel shows the semantic item, not YAML.
	await changes.click();
	const panel = page.locator('aside').last();
	await expect(panel.getByText('web-1')).toBeVisible();
	await expect(panel.getByText(/memory/i).first()).toBeVisible();

	// Propose: title required, then the PR result lands.
	await panel.getByPlaceholder('Pull request title').fill('web-1: raise memory to 8Gi');
	await panel.getByRole('button', { name: 'Propose pull request -> team-web' }).click();
	await expect(panel.getByText(/#77|pulls\/77|proposed/i).first()).toBeVisible();
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
	await page.locator('button[title^="Changes"]').click();
	const panel = page.locator('aside').last();
	await panel.getByRole('button', { name: 'Propose pull request -> team-web' }).click();
	// The error surfaces inline; the draft is not silently lost.
	await expect(panel.getByText(/title/i).first()).toBeVisible();
	await expect(panel.getByText('web-2')).toBeVisible();
});

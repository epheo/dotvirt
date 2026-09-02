import { expect, login, setScenario, test } from './fx';

// Releasing a repoless project: the way BACK from the "no repo configured"
// dead-end. The label residue is stripped, the project dissolves, and its
// namespaces (still running VMs) return to Existing tenants.

test('release dissolves a repoless project back to an adoptable namespace', async ({ page }) => {
	await setScenario(page, 'residue');
	await login(page);

	const tree = page.locator('aside');
	await expect(tree.getByText('oldportal', { exact: true })).toBeVisible();

	await tree.getByText('oldportal', { exact: true }).click({ button: 'right' });
	await page.getByRole('button', { name: 'Release project…' }).click();

	// Typed confirm gates the verb; the modal names both paths.
	const modal = page.getByRole('dialog');
	await expect(modal.getByText(/no backing repo/)).toBeVisible();
	await modal.getByPlaceholder('oldportal').fill('oldportal');
	await modal.getByRole('button', { name: 'Release', exact: true }).click();

	await expect(page.getByText(/oldportal released/)).toBeVisible();
	// The project leaves the tree; the namespace comes back as adoptable.
	await expect(tree.getByText('oldportal', { exact: true })).toBeHidden();
	await expect(tree.getByText('oldportal-ns')).toBeVisible();
	await expect(tree.getByRole('button', { name: 'Adopt' }).first()).toBeVisible();
});

import { expect, login, setScenario, test } from './fx';

// The GitOps trouble states, all at once (scenario: drift). Each of these is a
// state a live cluster is rarely in when a human is watching — and each has
// regressed at least once before it grew an explicit contract.

test.beforeEach(async ({ page }) => {
	await setScenario(page, 'drift');
	await login(page);
});

test('standing sync failure surfaces on bell, tree and project', async ({ page }) => {
	// The issues bell counts the standing problems (project failure + bad VM).
	const bell = page.locator('button[title^="Issues"]');
	await expect(bell.locator('span').last()).toHaveText(/\d+/);
	await bell.click();
	await expect(page.getByText('Sync failed').first()).toBeVisible();
	await expect(page.getByText(/admission webhook/).first()).toBeVisible();
	await page.keyboard.press('Escape');

	// The tree badge honors the standing-failure gate: team-web carries one,
	// team-db (healthy, despite a Pending VM) must not.
	const aside = page.locator('aside');
	await expect(aside.locator('[title*="issue"]').first()).toBeVisible();
	const dbRow = aside.locator('a', { hasText: 'team-db' }).first();
	await expect(dbRow.locator('[title*="issue"]')).toHaveCount(0);
});

test('Pending is a waiting state, never a failure', async ({ page }) => {
	// A merged VM awaiting its first Argo sync reads "Pending sync" — showing
	// NotTracked here made adoption look failed (the bug that minted the state).
	await page.locator('aside').getByText('db-prod').click();
	const main = page.locator('main');
	await main.getByRole('link', { name: 'VMs', exact: true }).click();
	const row = main.locator('tbody tr', { hasText: 'db-2' });
	await expect(row).toContainText('Pending');
	await expect(row).not.toContainText('Not tracked');
});

test('untracked VM in a healthy project offers adoption where the user is', async ({ page }) => {
	await page.locator('aside').getByText('team-db', { exact: true }).click();
	const main = page.locator('main');
	// The adopt banner carries the next action (guided, not documented).
	await expect(main.getByText(/not described in git/)).toBeVisible();
	await expect(main.getByRole('button', { name: 'Adopt into git' })).toBeVisible();
	// And the VM row itself says what it is.
	await main.getByRole('link', { name: 'VMs', exact: true }).click();
	await expect(main.locator('tbody tr', { hasText: 'legacy-1' })).toContainText('Not tracked');
});

test('prune risk warns before anything merges', async ({ page }) => {
	await page.locator('button[title^="Changes"]').click();
	const panel = page.locator('aside').last();
	await expect(panel.getByText(/Merging will prune 2 objects/)).toBeVisible();
	await expect(panel.getByText('web-1')).toBeVisible();
});

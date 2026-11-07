import { expect, login, openTray, setScenario, test } from './fx';

// The GitOps trouble states, all at once (scenario: drift). Each of these is a
// state a live cluster is rarely in when a human is watching — and each has
// regressed at least once before it grew an explicit contract.

test.beforeEach(async ({ page }) => {
	await setScenario(page, 'drift');
	await login(page);
});

test('standing sync failure surfaces on bell, tree and project', async ({ page }) => {
	// The header counts the standing problems (project failure + bad VM) in words.
	const bell = page.locator('button[title^="Issues"]');
	await expect(bell).toHaveText(/\d+ issues?/);
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

test('a platform-tier problem is an issue that names the object and links to it', async ({
	page,
}) => {
	// The platform Application has no project row; its degraded uplink policy
	// must still reach the bell by name, with Argo's reason, and open the view
	// that shows it.
	const bell = page.locator('button[title^="Issues"]');
	await bell.click();
	const row = page.getByRole('button', { name: /dc-vlan-bridge/ }).first();
	await expect(row).toBeVisible();
	await expect(row).toHaveAttribute('title', /1\/1 nodes failed to configure/);
	await row.click();
	await expect(page).toHaveURL(/\/networking/);
});

test('Pending is a waiting state, never a failure', async ({ page }) => {
	// A merged VM awaiting its first Argo sync reads "Pending sync" — showing
	// NotTracked here made adoption look failed (the bug that minted the state).
	await page.locator('aside').getByText('db-prod').click();
	const main = page.locator('main');
	await main.getByRole('link', { name: 'VMs', exact: true }).click();
	const row = main.locator('tbody tr', { hasText: 'db-2' });
	await expect(row).toContainText('Pending');
	await expect(row).not.toContainText('Not in git');
});

test('untracked VM in a healthy project offers adoption where the user is', async ({ page }) => {
	await page.locator('aside').getByText('team-db', { exact: true }).click();
	const main = page.locator('main');
	// The adopt banner carries the next action (guided, not documented).
	await expect(main.getByText(/not described in git/)).toBeVisible();
	await expect(main.getByRole('button', { name: 'Adopt into git' })).toBeVisible();
	// And the VM row itself says what it is.
	await main.getByRole('link', { name: 'VMs', exact: true }).click();
	await expect(main.locator('tbody tr', { hasText: 'legacy-1' })).toContainText('Not in git');
});

test('prune risk needs an explicit yes before the PR opens', async ({ page }) => {
	const tray = await openTray(page);
	await expect(tray.getByText(/also deletes 2 objects/)).toBeVisible();
	await tray.getByRole('button', { name: 'Review and propose' }).click();
	const sheet = page.getByRole('dialog');
	await expect(sheet.getByText(/deletes 2 objects from the cluster/)).toBeVisible();
	await expect(sheet.getByText(/web-prod\/old-vm/)).toBeVisible();
	await expect(sheet.getByText(/web-prod\/old-net/)).toBeVisible();
	const open = sheet.getByRole('button', { name: 'Open pull request' });
	await expect(open).toBeDisabled();
	await expect(sheet.getByText('Tick the deletion box to continue')).toBeVisible();
	await sheet.getByLabel(/Delete these 2 when this merges/).check();
	await expect(open).toBeEnabled();
	await expect(sheet.getByText('Tick the deletion box to continue')).toHaveCount(0);
	// The full lane still carries Argo's own sentence.
	await page.keyboard.press('Escape');
	await tray.getByRole('link', { name: 'Open in Changes' }).click();
	await expect(page.locator('main').getByText(/Merging will prune 2 objects/)).toBeVisible();
});

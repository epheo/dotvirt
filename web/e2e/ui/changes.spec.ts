import { expect, login, setScenario, test } from './fx';

const MERGE_HASH = 'ab12cd34ef' + '0'.repeat(30);

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

	// Selecting the PR reviews its diff here and offers exactly one action: the
	// forge deep link. No in-app merge button exists.
	await laneRow.click();
	await expect(main.getByText('12Gi', { exact: true })).toBeVisible();
	await expect(main.getByText('yours')).toBeVisible();
	const link = main.getByRole('link', { name: /Open PR to approve and merge/ });
	await expect(link).toBeVisible();
	await expect(link).toHaveAttribute('href', /pulls\/41/);
	await expect(main.getByRole('button', { name: /^Merge/ })).toHaveCount(0);
	await expect(main.getByText(/Approval and merge happen in the forge/)).toBeVisible();

	// A colleague's PR is in the lane too, attributed; the Mine filter hides it.
	const bobs = main.getByRole('button', { name: /PR #42/ });
	await expect(bobs).toContainText('by bob');
	await bobs.click();
	await expect(main.getByText('by bob').last()).toBeVisible();
	await expect(main.getByText('Excluded', { exact: true })).toBeVisible();
	await main.getByRole('button', { name: 'Mine', exact: true }).click();
	await expect(bobs).toHaveCount(0);
	await expect(laneRow).toBeVisible();
	await main.getByRole('button', { name: 'All', exact: true }).click();
	await expect(bobs).toBeVisible();
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
	await main.getByRole('link', { name: /^History/ }).click();
	await main.getByRole('button', { name: /^team-web$/ }).click();
	// A forge merge is named by the PR it merged, not the merge boilerplate.
	const row = main.getByRole('button', { name: /web-2: add data disk/ });
	await expect(row).toBeVisible();
	await row.click();

	await expect(main.getByRole('link', { name: /PR #40/ })).toHaveAttribute('href', /pulls\/40/);
	await expect(main.getByText(/data \(50Gi\)/)).toBeVisible();

	// Undo confirms in a dialog and yields a pull request, never a direct change.
	await main.getByRole('button', { name: 'Undo this change' }).click();
	const dialog = page.getByRole('dialog', { name: /Undo this change/ });
	await expect(dialog.getByText(/web-2: add data disk/)).toBeVisible();
	await dialog.getByRole('button', { name: 'Open pull request' }).click();
	await expect(dialog).toHaveCount(0);
	// The stepper and the note both link the revert PR.
	await expect(main.getByRole('link', { name: /PR #78/ }).first()).toHaveAttribute(
		'href',
		/pulls\/78/,
	);
	await expect(main.getByText(/Approve and merge it in the forge/)).toBeVisible();
});

test('a review is deep-linkable, and the VM page links its own history', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto(`/changes?project=team-web&commit=${MERGE_HASH}`);

	const main = page.locator('main');
	// The commit is under review without a click, its project history open beside it.
	await expect(main.getByText(/data \(50Gi\)/)).toBeVisible();
	await expect(main.getByRole('button', { name: /web-2: add data disk/ })).toBeVisible();
	// The manifest reads as a text diff of the two committed sides.
	await main.getByText('Manifest diff').click();
	await expect(main.getByText(/^\+\s+- name: data$/)).toBeVisible();
	await expect(main.getByText(/^-\s+/)).toHaveCount(0);
	// Selecting a PR moves the URL with it.
	await main.getByRole('link', { name: /^In review/ }).click();
	await main.getByRole('button', { name: /PR #41/ }).click();
	await expect(page).toHaveURL(/\/changes\?project=team-db&pr=41$/);

	// The VM's Changes tab lists its versions; each title opens the same review.
	await page.goto('/vm/web-prod/web-2?tab=changes');
	const row = page.locator('main').getByRole('link', { name: /web-2: add data disk/ });
	await expect(row).toBeVisible();
	await row.click();
	await expect(page).toHaveURL(new RegExp(`/changes\\?project=team-web&commit=${MERGE_HASH}$`));
	await expect(page.locator('main').getByText(/data \(50Gi\)/)).toBeVisible();
});

test('one job per tab: the page opens on what waits and says so', async ({ page }) => {
	await setScenario(page, 'drift');
	await login(page);
	await page.goto('/changes');

	const main = page.locator('main');
	// A staged draft opens on Staged; the header names the refused merge and
	// where to deal with it.
	await expect(main.getByRole('link', { name: /^Staged\s*1$/ })).toHaveAttribute(
		'aria-current',
		'page',
	);
	await expect(main.getByText(/The cluster refused the last merge in team-web/)).toBeVisible();
	await expect(main.getByText('1 staged, 0 in review, 2 projects')).toBeVisible();
	await main.getByRole('link', { name: 'Open history' }).click();
	await expect(page).toHaveURL(/\/changes\/team-web\?tab=history$/);
	await expect(main.getByRole('button', { name: /web-2: add data disk/ })).toBeVisible();
	// Unstage and discard say what they drop before anything is sent.
	await main.getByRole('link', { name: /^Staged/ }).click();
	await expect(main.getByText(/Drops this change from the team-web draft/)).toBeVisible();
	await main.getByRole('button', { name: 'Discard all' }).click();
	await expect(
		main.getByText('Drops every staged change in team-web. Nothing reaches git.'),
	).toBeVisible();
	await main.getByRole('button', { name: 'Keep' }).click();
});

test('at scale, history pages its projects and the tree folds', async ({ page }) => {
	test.setTimeout(90_000);
	await setScenario(page, 'scale');
	await login(page);
	await page.goto('/changes?tab=history');

	const main = page.locator('main');
	const aside = page.locator('aside');
	await expect(main.getByRole('navigation', { name: 'Pages' })).toContainText('Showing 1-50 of 53');
	await main.getByLabel('Find a project').fill('team-w');
	await expect(main.getByRole('button', { name: /^team-web$/ })).toBeVisible();
	await expect(main.getByRole('button', { name: /^analytics$/ })).toHaveCount(0);
	// The rail keeps what is in flight and folds the rest; an open namespace
	// lists its first VMs.
	await expect(aside.getByRole('button', { name: '47 more projects' })).toBeVisible();
	await aside.locator('a[href="/changes/team-web?tab=history"]').click();
	await aside.locator('a[href="/changes/team-web/web-prod?tab=history"]').click();
	await expect(aside.getByRole('link', { name: /^\d+ more VMs$/ })).toBeVisible();
});

test('a failed drafts read says so in the Staged card and retries', async ({ page }) => {
	await setScenario(page, 'draftsdown');
	await login(page);
	await page.goto('/changes');
	const main = page.locator('main');
	await expect(
		main.getByText(/Could not read your staged changes: drafts unavailable/),
	).toBeVisible();
	await expect(main.getByRole('button', { name: 'Retry' })).toBeEnabled();
	await expect(main.getByText('Nothing staged')).toHaveCount(0);
});

test('a platform stager without platform read reviews their own platform PRs', async ({ page }) => {
	await setScenario(page, 'platstager');
	await login(page);
	await page.goto('/changes/platform?tab=review');
	const main = page.locator('main');
	// The tree reaches the platform through the caller's own PRs.
	await expect(page.locator('aside').getByRole('link', { name: /^platform/ })).toBeVisible();

	await main.getByRole('button', { name: /PR #7/ }).click();
	await expect(main.getByText('dc-vlan-120').first()).toBeVisible();
	await expect(main.getByText('yours')).toBeVisible();

	// Changed outside dotvirt: the server's answer is the review, with nothing to retry.
	await main.getByRole('button', { name: /PR #8/ }).click();
	await expect(
		main.getByText(/pull request #8 changed outside dotvirt since you proposed it/),
	).toBeVisible();
	await expect(main.getByRole('button', { name: 'Retry' })).toHaveCount(0);
	await expect(main.getByRole('link', { name: /Open PR to approve and merge/ })).toHaveAttribute(
		'href',
		/platform\/pulls\/8/,
	);

	// History stays the readers'; it says so and points back at the PRs.
	await page.goto('/changes/platform?tab=history');
	await expect(main.getByText("You cannot read the platform's history.")).toBeVisible();
	await main.getByRole('link', { name: 'Your pull requests there are under In review' }).click();
	await expect(main.getByRole('button', { name: /PR #7/ })).toBeVisible();
});

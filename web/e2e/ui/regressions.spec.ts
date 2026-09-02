import { expect, login, openVM, setScenario, test } from './fx';

// One test per UX bug the git history already paid for once. Each names the
// PR that fixed it; the fixture holds the state that triggered it.

test('#151: sizing below the preference minimum refuses at stage time', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await openVM(page, 'web-1');
	await page.getByRole('button', { name: /Edit Settings/ }).click();

	// windows.11 requires 2 vCPUs / 4Gi; understaging must fail HERE, not at
	// KubeVirt's webhook three merges later.
	await page.getByLabel('Preference').selectOption('windows.11');
	await page.getByLabel('Memory').fill('1Gi');
	await expect(page.getByText(/requires at least 4Gi/)).toBeVisible();

	const finish = page.getByRole('button', { name: 'Stage change' });
	for (let i = 0; i < 12 && !(await finish.isVisible()); i++) {
		await page.getByRole('button', { name: 'Next', exact: true }).click();
	}
	await expect(finish).toBeDisabled();
});

test('#116: the review step names every unmet field', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.getByRole('button', { name: /^New$/ }).click();
	await page.getByRole('button', { name: 'New VM', exact: true }).click();

	// Navigation is free; jumping straight to the end must summarize what is
	// missing instead of a dead disabled button.
	await page.getByRole('button', { name: 'Ready to complete' }).click();
	await expect(page.getByText('Complete the required fields to stage this VM:')).toBeVisible();
	await expect(page.getByText(/• Name/).first()).toBeVisible();
	await expect(
		page.getByRole('button', { name: /Stage VM|Create VM|Stage/ }).last(),
	).toBeDisabled();
});

test('#142: the hosts tree lists VM-less nodes too', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.locator('aside').getByRole('link', { name: 'Hosts' }).click();
	// worker-3 runs nothing (cordoned for maintenance) - seeding the tree from
	// VM rows alone made empty hosts invisible, exactly when they matter.
	await expect(page.locator('aside').getByText('worker-3')).toBeVisible();
});

test('#144: classless disks group under the real default class', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.locator('aside').getByRole('link', { name: 'Storage', exact: true }).click();
	// Every fixture disk omits storageClass; the lens must resolve them to the
	// cluster default (fast-ssd), not a placeholder bucket.
	const aside = page.locator('aside');
	await expect(aside.getByText('fast-ssd')).toBeVisible();
	await aside.getByText('fast-ssd').click();
	await expect(page.locator('main').getByText('web-1')).toBeVisible();
});

test('#123: global search reaches segments and storage classes', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.keyboard.press('/');
	await page.keyboard.type('lan-101');
	await page.locator('li button', { hasText: 'lan-101' }).first().click();
	await expect(page).toHaveURL(/\/networking\//);

	await page.keyboard.press('/');
	await page.keyboard.type('fast-ssd');
	await page.locator('li button', { hasText: 'fast-ssd' }).first().click();
	await expect(page).toHaveURL(/\/storage\/fast-ssd/);
});

test('#154: a converged app retires its old operation failure', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	// team-batch carries operation=Failed + an old syncError, but sync=Synced:
	// history, not a standing problem - no bell entry, no tree badge.
	await expect(page.locator('aside').getByText('team-batch')).toBeVisible();
	await page.locator('button[title^="Issues"]').click();
	await expect(page.getByText('No standing issues.')).toBeVisible();
});

test('#119: hand-written affinity refuses the scheduling form', async ({ page }) => {
	await setScenario(page, 'drift');
	await login(page);
	await page.locator('aside').getByText('db-prod').click();
	await openVM(page, 'pinned-legacy');
	await page.getByRole('button', { name: /Edit Settings/ }).click();
	await page.getByRole('button', { name: 'Scheduling' }).click();
	await expect(page.getByText(/hand-written affinity/)).toBeVisible();
	await expect(page.getByRole('button', { name: '+ Add group' })).toHaveCount(0);
});

test('#146: SSO pending guides instead of offering a failing button', async ({ page }) => {
	await setScenario(page, 'sso');
	await page.goto('/');
	await expect(page.getByText(/enabled but not ready/)).toBeVisible();
	await expect(page.getByRole('link', { name: 'Sign in with OpenShift' })).toHaveCount(0);
	// The token path must stay available for the admin who will fix it.
	await expect(page.locator('textarea')).toBeVisible();

	await setScenario(page, 'ssoready');
	await page.reload();
	await expect(page.getByRole('link', { name: 'Sign in with OpenShift' })).toBeVisible();
});

test('bulk selection stages a power change per VM', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.locator('main').getByRole('link', { name: 'VMs', exact: true }).click();
	await page.getByLabel('Select web-1').check();
	await page.getByLabel('Select web-2').check();
	await page.locator('main tbody tr', { hasText: 'web-1' }).click({ button: 'right' });
	await expect(page.getByText('2 VMs selected')).toBeVisible();
	await page.getByRole('button', { name: 'Power Off (staged)' }).click();
	// Deliberate skip: web-2 is already Off, so only web-1 stages - a bulk op
	// must not write no-op edits into the draft.
	await page.getByRole('link', { name: /Review changes/ }).click();
	const staged = page.locator('main [data-project]');
	await expect(staged).toHaveCount(1);
	await expect(staged.first()).toContainText('web-1');
});

test('stopped VMs are visible in the summary tile', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	// The live console showed "2 Running" on a 3-VM project: the phase map came
	// from VMI metrics and stopped VMs (no VMI) were invisible. The backend now
	// folds them in from the snapshot; the tile must show both.
	const main = page.locator('main');
	await expect(main.getByText('Running', { exact: true })).toBeVisible();
	await expect(main.getByText('Stopped', { exact: true })).toBeVisible();
});

test('an empty scope reads "no data", not zero-of-zero', async ({ page }) => {
	await setScenario(page, 'empty');
	await login(page);
	await expect(page.locator('main').getByText('no data').first()).toBeVisible();
});

test('tab scroll regions are keyboard-reachable', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	// axe scrollable-region-focusable, found live on the Permissions tab: the
	// scroll container carries tabindex so keyboard users can scroll it.
	await expect(page.locator('main [role="region"][tabindex="0"]').first()).toBeVisible();
});

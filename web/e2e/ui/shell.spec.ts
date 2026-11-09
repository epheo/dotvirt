import { expect, login, setScenario, test } from './fx';

// Shell + inventory smoke against the base scenario, plus the empty-cluster
// onboarding state — hermetic versions of the live-stack smoke suite.

test('base: shell, tree and VM table render from the stream', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);

	const aside = page.locator('aside');
	await expect(aside.getByText('team-web')).toBeVisible();
	await expect(aside.getByText('team-db')).toBeVisible();

	const main = page.locator('main');
	await main.getByRole('link', { name: /^VMs [\d,]+$/ }).click();
	await expect(main.getByRole('link', { name: 'web-1', exact: true })).toBeVisible();
	await expect(main.getByRole('link', { name: 'web-2', exact: true })).toBeVisible();

	// The stopped VM reads as stopped, not as an error state.
	const stopped = main.locator('tbody tr', { hasText: 'web-2' });
	await expect(stopped).toContainText(/Stopped|Off/);

	// A healthy fleet has no GitOps problem; its one firing cluster alert is
	// the only issue, named by VM, with the step that looks into it.
	const issues = page.locator('button[title^="Issues"]');
	await expect(issues).toHaveText('1 issue');
	await issues.click();
	await expect(
		page.getByRole('button', { name: /web-1 web-prod VMI excessive migrations/ }),
	).toBeVisible();
	await expect(
		page.getByRole('banner').getByRole('button', { name: 'Open monitor' }),
	).toBeVisible();
	// The VM's row offers the same step.
	await expect(
		main.locator('tbody tr', { hasText: 'web-1' }).getByRole('button', { name: 'Open monitor' }),
	).toBeVisible();
});

test('base: VM detail opens with live facts and tabs', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page
		.locator('main')
		.getByRole('link', { name: /^VMs [\d,]+$/ })
		.click();
	await page
		.locator('tbody tr', { hasText: 'web-1' })
		.first()
		.getByRole('link', { name: 'web-1', exact: true })
		.click();

	await expect(page).toHaveURL(/\/vm\/web-prod\/web-1/);
	await expect(page.getByRole('button', { name: /Edit Settings/ })).toBeVisible();
	// Where it runs, in one line under the name; the file it lives in, with its history.
	await expect(page.getByText(/Running on worker-1, up \d/)).toBeVisible();
	const source = page.locator('main tr', { hasText: 'Source file' });
	await expect(source).toContainText('vms/web-1.yaml');
	await expect(source.getByRole('link', { name: 'History' })).toBeVisible();
	await expect(page.locator('main').getByText('worker-1').first()).toBeVisible();
	await expect(page.locator('main').getByText('10.128.0.10').first()).toBeVisible();
	await expect(page.getByRole('link', { name: 'Snapshots', exact: true })).toBeVisible();
	await expect(page.getByRole('link', { name: 'Console', exact: true })).toBeVisible();

	// The flat toolbar: power first (declarative - the button STAGES a change),
	// then the imperative verbs; delete demoted into the Actions menu.
	await expect(page.getByRole('button', { name: 'Power off', exact: true })).toBeVisible();
	await expect(page.getByRole('button', { name: 'Restart', exact: true })).toBeVisible();
	await expect(page.getByRole('button', { name: 'Migrate', exact: true }).first()).toBeVisible();
	await expect(page.getByRole('button', { name: 'Delete VM' })).toHaveCount(0);
	await page.getByRole('button', { name: 'Power off', exact: true }).click();
	// The stage lands as a toast + staged badge, never an immediate power change.
	await expect(page.getByText(/Power Off staged for web-1/)).toBeVisible();
	await expect(page.getByRole('button', { name: '1 staged, not proposed' })).toBeVisible();
	// The Changes tab counts what is in flight for this VM: the staged power
	// change and bob's open PR #42, which also touches web-1.
	await expect(page.getByRole('link', { name: /^Changes\s*2$/ })).toBeVisible();
	await page.getByRole('button', { name: 'Actions' }).click();
	await expect(page.getByRole('button', { name: 'Delete VM' })).toBeVisible();
	await page.keyboard.press('Escape');
});

test('empty: onboarding CTA instead of a dead-end blank tree', async ({ page }) => {
	await setScenario(page, 'empty');
	await login(page);

	await expect(page.getByText('No projects visible.')).toBeVisible();
	// Guided-user rule: the empty state carries its next action.
	await expect(page.getByRole('button', { name: 'Create your first project' })).toBeVisible();
});

test('deep links survive a hard reload', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/vm/web-prod/web-1');
	await expect(page.getByRole('button', { name: /Edit Settings/ })).toBeVisible();
	await page.reload();
	await expect(page.getByRole('button', { name: /Edit Settings/ })).toBeVisible();
	await page.goto('/compute?tab=templates&item=team-web%2Fweb-node');
	await page.reload();
	await expect(page.locator('main aside').getByRole('heading', { name: 'web-node' })).toBeVisible();
});

test('base: the summary shows changes in scope; the table names open PRs per VM', async ({
	page,
}) => {
	await setScenario(page, 'base');
	await login(page);
	const main = page.locator('main');
	// Issues beside capacity, and capacity leads with its answer.
	await expect(main.getByText('Issues in All VMs')).toBeVisible();
	await expect(main.getByText('Room for 21 more VMs')).toBeVisible();
	await expect(main.getByText('worker-3, in maintenance')).toBeVisible();
	const card = main.locator('section', { hasText: 'Changes in this scope' });
	await expect(card.getByText(/PR #41, yours/)).toBeVisible();
	await expect(card.getByText(/PR #42 by bob/)).toBeVisible();
	// The merge feed's PR #40 is the applied revision, so it reads live.
	await expect(card.locator('li', { hasText: 'PR #40' })).toContainText('Live');

	await main.getByRole('link', { name: /^VMs [\d,]+$/ }).click();
	const web1 = main.locator('tbody tr', { hasText: 'web-1' }).first();
	await expect(web1.getByRole('link', { name: /PR #42 by bob: balancer/ })).toBeVisible();
	const db1 = main.locator('tbody tr', { hasText: 'db-1' }).first();
	await expect(db1.getByRole('link', { name: /PR #41: memory 8Gi to 12Gi/ })).toBeVisible();
});

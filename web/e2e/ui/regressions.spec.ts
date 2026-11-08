import { expect, login, openTray, openVM, setScenario, stageChange, test } from './fx';

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
	await page.getByRole('dialog').getByLabel('Memory').fill('1Gi');
	await expect(page.getByText(/requires at least 4Gi/)).toBeVisible();

	const finish = page.getByRole('button', { name: 'Stage change' });
	for (let i = 0; i < 12 && !(await finish.isVisible()); i++) {
		await page.getByRole('button', { name: 'Next', exact: true }).click();
	}
	await expect(finish).toBeDisabled();
});

test('#116: a disabled Stage names what it waits for', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.getByRole('button', { name: /^New$/ }).click();
	const sheet = page.getByRole('dialog');
	await sheet.getByLabel('Name', { exact: true }).fill('');
	await expect(sheet.getByRole('status')).toHaveText('Name the VM');
	await expect(sheet.getByRole('button', { name: 'Stage VM' })).toBeDisabled();
	await sheet.getByLabel('Name', { exact: true }).fill('Bad_Name');
	await expect(sheet.getByRole('status')).toContainText('lowercase');
});

test('New from inside a namespace preselects that namespace', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	// batch-prod sorts before web-prod, so a first-of-list default would miss.
	await page.goto('/compute/team-web/web-prod');
	await page.getByRole('button', { name: /^New$/ }).click();
	const sheet = page.getByRole('dialog');
	await expect(sheet.getByText('Filled in from web-prod, where you were.')).toBeVisible();
	await expect(sheet.getByLabel('Project and namespace')).toHaveText(/team-web \/ web-prod/);
	// The place survives a switch of what to create.
	await sheet.getByRole('radio', { name: 'Image' }).click();
	await expect(sheet.getByLabel('Project and namespace')).toHaveText(/team-web \/ web-prod/);
	await sheet.getByRole('radio', { name: 'Network', exact: true }).click();
	await expect(sheet.getByLabel('Project', { exact: true })).toHaveText(/team-web/);
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
	await page.locator('main').getByRole('link', { name: 'Show the 5 disks' }).click();
	await expect(page.locator('main').getByRole('link', { name: 'web-1' })).toBeVisible();
});

test('storage summary shows platform facts beside the VM footprint', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.locator('aside').getByRole('link', { name: 'Storage', exact: true }).click();
	await page
		.locator('main')
		.getByRole('link', { name: /^Classes/ })
		.click();
	const table = page.locator('main').getByRole('table');
	// The local class: per-node free capacity summed, single-attach disks.
	const fast = table.getByRole('row').filter({ hasText: 'fast-ssd' });
	await expect(fast).toContainText('2.9 TiB');
	await expect(fast).toContainText('Not possible');
	// The shared class lists with its facts.
	const bulk = table.getByRole('row').filter({ hasText: 'bulk-hdd' });
	await expect(bulk).toContainText('Possible');
	await expect(bulk).toContainText('nfs.csi.k8s.io');
	// The class page repeats the facts and lists every provisioned disk.
	await fast.getByRole('link', { name: 'fast-ssd' }).click();
	await expect(page).toHaveURL(/\/storage\/fast-ssd/);
	const main = page.locator('main');
	await expect(main.getByText('WaitForFirstConsumer')).toBeVisible();
	await expect(main.getByText('Free on worker-2')).toBeVisible();
	await expect(main.getByRole('heading', { name: 'Disks' })).toBeVisible();
});

test('#123: global search reaches networks and storage classes', async ({ page }) => {
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
	// history, not a standing problem - no issue, no tree badge, and never an
	// offer to recover its healthy repo.
	const aside = page.locator('aside');
	await expect(aside.getByText('team-batch')).toBeVisible();
	await expect(
		aside.locator('a', { hasText: 'team-batch' }).locator('[title*="issue"]'),
	).toHaveCount(0);
	await page.locator('button[title^="Issues"]').click();
	await expect(page.getByRole('button', { name: /team-batch/ })).toHaveCount(0);
	await expect(page.getByRole('button', { name: 'Recover repo' })).toHaveCount(0);
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
	await page
		.locator('main')
		.getByRole('link', { name: /^VMs [\d,]+$/ })
		.click();
	await page.getByLabel('Select web-1').check();
	await page.getByLabel('Select web-2').check();
	await page.locator('main tbody tr', { hasText: 'web-1' }).click({ button: 'right' });
	await expect(page.getByText('2 VMs selected')).toBeVisible();
	await page.getByRole('button', { name: 'Power Off (staged)' }).click();
	// Deliberate skip: web-2 is already Off, so only web-1 stages - a bulk op
	// must not write no-op edits into the draft.
	await (await openTray(page)).getByRole('link', { name: 'Open in Changes' }).first().click();
	const staged = page.locator('main [data-project]');
	await expect(staged).toHaveCount(1);
	await expect(staged.first()).toContainText('web-1');
});

test('stopped VMs are visible in the summary tile', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	// The live console showed "2 Running" on a 3-VM project: the phase map came
	// from VMI metrics and stopped VMs (no VMI) were invisible. The stat line
	// counts the live inventory, so both must show.
	const main = page.locator('main');
	await expect(main.getByText(/\d+ running/)).toBeVisible();
	await expect(main.getByText(/\d+ stopped/)).toBeVisible();
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

test('a drafts refresh keeps the half-typed PR title', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await openVM(page, 'web-1');
	await page.getByRole('button', { name: /Edit Settings/ }).click();
	await page.getByRole('dialog').getByLabel('Memory').fill('8Gi');
	await stageChange(page);
	await (await openTray(page)).getByRole('link', { name: 'Open in Changes' }).first().click();

	const main = page.locator('main');
	const title = main.getByLabel('Pull request title');
	await title.fill('web-1: raise memory to 8Gi');
	// The dock's Refresh re-pulls every draft. The propose form was keyed on the
	// selection object, rebuilt by that pull, and emptied itself mid-typing.
	const refreshed = page.waitForResponse((r) => r.url().includes('/api/drafts'));
	await page.getByTitle('Refresh').click();
	await refreshed;
	await expect(main.getByText('web-prod/web-1')).toBeVisible();
	await expect(title).toHaveValue('web-1: raise memory to 8Gi');
});

test('context-menu Edit settings on another VM opens on that VM', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await openVM(page, 'web-1');
	// From web-1's page, the tree's context menu on web-2 must land on web-2 with
	// the dialog open: the dialog is the shell's, so leaving the page cannot
	// drop it.
	await page.locator('aside').getByText('web-2', { exact: true }).click({ button: 'right' });
	await page.getByRole('button', { name: 'Edit settings', exact: true }).click();
	await expect(page).toHaveURL(/\/vm\/web-prod\/web-2/);
	await expect(page.getByRole('dialog').getByLabel('Memory')).toBeVisible();
});

test('the click that closes a context menu does not select the row under it', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page
		.locator('main')
		.getByRole('link', { name: /^VMs [\d,]+$/ })
		.click();
	const rows = page.locator('main tbody tr');
	// Open from the row's last cell so the menu hangs clear of the name cells.
	await rows.filter({ hasText: 'web-1' }).locator('td').last().click({ button: 'right' });
	const menu = page.getByRole('button', { name: 'Edit settings', exact: true });
	await expect(menu).toBeVisible();
	// The backdrop that used to swallow this click was replaced by window
	// listeners that let it through to the grid, so the row also peeked.
	const web2 = rows.filter({ hasText: 'web-2' }).getByText('web-2', { exact: true });
	await web2.click();
	await expect(menu).toBeHidden();
	await expect(page.getByRole('complementary', { name: 'VM inspector' })).toHaveCount(0);
	await expect(page).not.toHaveURL(/peek=/);
	// A right-click on another row still swaps menus in one gesture.
	await web2.click({ button: 'right' });
	await expect(menu).toBeVisible();
});

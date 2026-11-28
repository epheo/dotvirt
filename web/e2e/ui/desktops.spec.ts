import { expect, login, setScenario, test } from './fx';

// The Desktops section, read only: its own tile beside Compute, a tree of
// projects, namespaces, pools and desktops made one by one, and pages that
// say what the caller may not list instead of showing an empty table.

const rail = (page: import('@playwright/test').Page) => page.locator('aside').first();

test('desktops is a sixth tile, the six three over three', async ({ page }) => {
	await setScenario(page, 'desktops');
	await login(page);
	await page.goto('/desktops');
	const tiles = rail(page).locator('nav a');
	await expect(tiles).toHaveText([
		'Compute',
		'Desktops',
		'Hosts',
		'Networking',
		'Storage',
		'Changes',
	]);
	await expect(tiles.nth(1)).toHaveAttribute('aria-current', 'page');
	const tops = await tiles.evaluateAll((as) =>
		as.map((a) => Math.round(a.getBoundingClientRect().top)),
	);
	expect(new Set(tops.slice(0, 3)).size).toBe(1);
	expect(new Set(tops.slice(3)).size).toBe(1);
	expect(tops[3]).toBeGreaterThan(tops[0]);
});

test("the tree walks projects to pools; a pool's desktops are rows of its page only", async ({
	page,
}) => {
	await setScenario(page, 'desktops');
	await login(page);
	let pulls = 0;
	page.on('request', (r) => {
		if (new URL(r.url()).pathname === '/api/desktops') pulls++;
	});
	await page.goto('/desktops');
	const tree = rail(page).locator('nav ~ div a');
	await expect(tree).toHaveText([
		'All desktops 5',
		'Desktop classes 2',
		'team-web 4',
		'web-prod 4',
		'eng 2/4',
		'lab 0/2',
		'carol-dev',
		'team-db 1',
		'db-prod 1',
		'ops-desk',
		// No count where the caller may not list desktops: a 0 would read as none.
		'team-batch',
		'batch-prod',
		'render closed 1/3',
	]);
	await expect(rail(page).getByText('Desktops not shown to you')).toBeVisible();
	await expect(rail(page).locator('span[title="Stopped"]')).toBeVisible();

	await rail(page).getByRole('link', { name: /^eng/ }).click();
	await expect(page).toHaveURL(/\/desktops\/team-web\/web-prod\/pool\/eng$/);
	const main = page.locator('main');
	await expect(main.getByRole('navigation', { name: 'Breadcrumb' })).toHaveText(
		/Desktops\s*\/\s*team-web\s*\/\s*web-prod\s*\/\s*eng/,
	);
	await expect(main.getByText('Ready', { exact: true }).first()).toBeVisible();
	const rows = main.getByRole('table', { name: 'Desktops of eng' }).locator('tbody tr');
	await expect(rows).toHaveCount(3);
	await expect(rows.nth(0)).toContainText('alice');
	await expect(rows.nth(0)).toContainText('In session');
	await expect(rows.nth(0)).toContainText(/\d+m ago/);
	await expect(rows.nth(1)).toContainText('bob');
	await expect(rows.nth(1)).toContainText('Stopped');
	await expect(rows.nth(2)).toContainText('Warm');
	// A pool's desktop opens no page of its own.
	await expect(rows.getByRole('link')).toHaveCount(0);
	// Walking the section reads the desktops once: only the watermark pulls again.
	expect(pulls).toBe(1);
});

test("a pool's seats never overlap, and its rules read in words", async ({ page }) => {
	await setScenario(page, 'desktops');
	await login(page);
	await page.goto('/desktops/team-web/web-prod/pool/eng');
	const main = page.locator('main');
	const seats = main.getByRole('region', { name: 'Seats' });
	const part = (title: string) => seats.getByTitle(title, { exact: true });
	// held 2 = 1 in session + 0 held without one + 1 stopped; with warm 1 and free 1, the 4 seats.
	await expect(part('Held desktops a client streams from')).toContainText('1');
	await expect(part('Held desktops running without a session')).toContainText('0');
	await expect(part('Held desktops their claim stopped')).toContainText('1');
	await expect(part('Running desktops no claim holds yet')).toContainText('1');
	await expect(part('Seats without a desktop')).toContainText('1');
	await expect(seats.getByText('Ready', { exact: true })).toHaveCount(0);

	const rules = main.getByRole('region', { name: 'Pool', exact: true });
	await expect(rules).toContainText('Group engineers');
	await expect(rules).toContainText('Stopped after 1 h without a session');
	await expect(rules).toContainText('Desktop and home deleted after 30 d without a session');
	await expect(rules).toContainText(/Ready\s*Yes$/);
	await expect(rules).not.toContainText('ClassFound');
});

test('a closed pool says so whatever its Ready, and its claims wait for it', async ({ page }) => {
	await setScenario(page, 'desktops');
	await login(page);
	await page.goto('/desktops/team-batch/batch-prod/pool/render');
	const main = page.locator('main');
	await expect(main.getByText(/^Shared pool of class cad in batch-prod, closed$/)).toBeVisible();
	await expect(main.getByText(/^Closed: claims without a desktop wait/)).toBeVisible();
	await expect(main.getByText(/claims wait until the pool is reopened/)).toBeVisible();
	await expect(main.getByText(/raise the seats/)).toHaveCount(0);
	const rules = main.getByRole('region', { name: 'Pool', exact: true });
	await expect(rules).toContainText('None, closed');
	await expect(rules).toContainText('Yes: claims without a desktop wait');
	await expect(rules).toContainText(/Idle timeout\s*The class's/);

	await page.goto('/desktops/team-batch/batch-prod');
	const pools = main.getByRole('table', { name: 'Pools' });
	await expect(pools.locator('tbody tr').first()).toContainText(/Ready\s*Closed/);
});

test('a kind the caller may not list says so, never an empty table', async ({ page }) => {
	await setScenario(page, 'desktops');
	await login(page);
	await page.goto('/desktops/team-batch/batch-prod/pool/render');
	const main = page.locator('main');
	await expect(
		main.getByText('Desktops here are not shown to you: a platform admin can let you read them.'),
	).toBeVisible();
	await expect(main.getByRole('table', { name: 'Desktops of render' })).toHaveCount(0);
	await expect(main.getByRole('region', { name: 'Desktops', exact: true })).not.toContainText(
		'0 desktops',
	);
	// Seats that read short of a release are no fault.
	await expect(main.getByRole('heading', { level: 1, name: 'render' })).toBeVisible();
	await expect(main.getByText('Ready', { exact: true }).first()).toBeVisible();
	await expect(main.getByText(/does not exist|not ready/)).toHaveCount(0);

	await page.goto('/desktops/team-batch/batch-prod');
	await expect(main.getByText(/^Desktops here are not shown to you/)).toBeVisible();
	await expect(main.getByText('1 pool in team-batch', { exact: true })).toBeVisible();
	await expect(main.getByText('No desktop here yet.')).toHaveCount(0);

	await page.goto('/desktops');
	await expect(main.getByText(/^Desktops in batch-prod are not shown to you/)).toBeVisible();
});

test('a project persona reads its pools; desktops and classes are not shown, never none', async ({
	page,
}) => {
	await setScenario(page, 'desktopsviewer');
	await login(page);
	await page.goto('/desktops');
	const main = page.locator('main');
	const tree = rail(page).locator('nav ~ div a');
	await expect(tree.nth(0)).toHaveText('All desktops');
	await expect(tree.nth(1)).toHaveText('Desktop classes');
	await expect(main.getByRole('link', { name: 'All desktops', exact: true })).toBeVisible();
	await expect(main.getByText('3 pools', { exact: true })).toBeVisible();
	await expect(main.getByText(/\d+ desktops?/)).toHaveCount(0);
	await expect(
		main.getByText(/^Desktops in batch-prod, db-prod, web-prod are not shown to you: a platform/),
	).toBeVisible();
	await expect(main.getByRole('table', { name: 'All desktops' })).toHaveCount(0);
	await expect(main.getByText(/yet/)).toHaveCount(0);

	await main.getByRole('link', { name: 'Desktop classes', exact: true }).click();
	await expect(
		main.getByText('Desktop classes are not shown to you: a cluster admin can let you read them.'),
	).toBeVisible();
	await expect(main.getByText(/No desktop class/)).toHaveCount(0);

	// Where it may list nothing, it is told so, never "none here" nor "deleted".
	for (const path of [
		'/desktops/team-db/db-prod',
		'/desktops/team-db/db-prod/pool/ops',
		'/desktops/team-db/db-prod/desktop/ops-desk',
	]) {
		await page.goto(path);
		await expect(main.getByText(/^Pools here are not shown to you: a project admin/)).toBeVisible();
		await expect(main.getByText(/yet|deleted|0 pools/)).toHaveCount(0);
	}
});

test('a pool whose class is missing says so and what to do', async ({ page }) => {
	await setScenario(page, 'desktops');
	await login(page);
	await page.goto('/desktops');
	const main = page.locator('main');
	await expect(main.getByText('1 pool without a class')).toBeVisible();
	await main.getByRole('link', { name: 'lab', exact: true }).first().click();
	await expect(page).toHaveURL(/\/desktops\/team-web\/web-prod\/pool\/lab$/);
	await expect(
		main.getByText(
			'Class gpu-lab does not exist, so this pool makes no desktop and its claims wait.',
		),
	).toBeVisible();
	await expect(main.getByText(/ask a cluster admin to add gpu-lab/)).toBeVisible();
	await expect(main.getByText('Classes on the cluster: cad, office.')).toBeVisible();
	// Its status leaves every other count out: each reads 0.
	const seats = main.getByRole('region', { name: 'Seats' });
	await expect(seats.getByTitle('Held desktops a client streams from')).toContainText('0');
	await expect(seats.getByTitle('Claims with no seat yet')).toContainText('1');
	await expect(main.getByRole('region', { name: 'Pool', exact: true })).toContainText(
		'No: class gpu-lab does not exist',
	);
	await expect(main.getByText('None is made until its class exists.')).toBeVisible();
});

test('a desktop made one by one has its page; the classes are a root tab', async ({ page }) => {
	await setScenario(page, 'desktops');
	await login(page);
	await page.goto('/desktops');
	await rail(page).getByRole('link', { name: 'carol-dev' }).click();
	await expect(page).toHaveURL(/\/desktops\/team-web\/web-prod\/desktop\/carol-dev$/);
	const main = page.locator('main');
	await expect(main.getByRole('heading', { level: 1, name: 'carol-dev' })).toBeVisible();
	await expect(main.getByText('Desktop of carol, class office')).toBeVisible();
	await expect(main.getByText('quay.io/example/desktop-office:1.4')).toBeVisible();

	// A pool's desktop is a row of its pool, never a page.
	await page.goto('/desktops/team-web/web-prod/desktop/eng-7hq2k');
	await expect(main.getByText(/is a desktop of pool/)).toBeVisible();
	await expect(main.getByRole('region', { name: 'Desktop', exact: true })).toHaveCount(0);

	await rail(page)
		.getByRole('link', { name: /^Desktop classes/ })
		.click();
	await expect(page).toHaveURL(/\/desktops\?tab=classes$/);
	const classes = main.getByRole('table', { name: 'Desktop classes' }).locator('tbody tr');
	await expect(classes).toHaveText([/^cad\s*quay/, /^office\s*quay/]);
	// All desktops leaves the tab.
	await rail(page)
		.getByRole('link', { name: /^All desktops/ })
		.click();
	await expect(page).toHaveURL(/\/desktops\?tab=desktops$/);
	await expect(main.getByRole('table', { name: 'All desktops' }).locator('tbody tr')).toHaveCount(
		5,
	);
});

test('beside a VM page the Desktops tree stays live', async ({ page }) => {
	await setScenario(page, 'desktops');
	await login(page);
	await page.goto('/desktops');
	const all = rail(page).locator('nav ~ div a').first();
	await expect(all).toHaveText('All desktops 5');
	await page.keyboard.press('Control+k');
	await page.getByLabel('Search inventory').fill('web-1');
	await page.locator('header').getByRole('button', { name: /web-1/ }).first().click();
	await expect(page).toHaveURL(/\/vm\/web-prod\/web-1/);
	// A watermark move pulls again: the tree follows the caller's new rights.
	await setScenario(page, 'desktopsviewer');
	await expect(all).toHaveText('All desktops');
});

test('New makes nothing in Desktops, and says why', async ({ page }) => {
	await setScenario(page, 'desktops');
	await login(page);
	const create = page.getByRole('button', { name: 'New', exact: true });
	for (const path of ['/desktops', '/desktops/team-web/web-prod/pool/eng']) {
		await page.goto(path);
		await expect(create).toBeDisabled();
		await expect(create).toHaveAttribute(
			'title',
			"Desktops are not made here yet: declare pools and desktops in a project's repo",
		);
	}
	await page.goto('/compute');
	await expect(create).toBeEnabled();
});

test('a caller who may read no desktop is told so, never left reading', async ({ page }) => {
	await setScenario(page, 'desktopsunread');
	await login(page);
	const main = page.locator('main');
	for (const path of ['/desktops', '/desktops/team-web/web-prod/pool/eng']) {
		await page.goto(path);
		await expect(main.getByText('No desktop is shown to you.')).toBeVisible();
		await expect(main.getByText('A platform admin can let you read its desktops.')).toBeVisible();
		await expect(main.getByText(/Reading|could not be read|yet/)).toHaveCount(0);
	}
	await expect(rail(page).getByText('No desktop is shown to you.')).toBeVisible();
});

test('without desktops the section leaves for Compute', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/desktops');
	await expect(page).toHaveURL(/\/compute$/);
	await expect(rail(page).locator('nav a', { hasText: 'Desktops' })).toHaveCount(0);
});

// The acts on a pool's desktop run now, under the caller's account, on the
// claim holding it: the one shown when its menu opened, named by its UID.
// What they change arrives as DeskuBe writes it, on the next pull.

const poolRows = (page: import('@playwright/test').Page) =>
	page.locator('main').getByRole('table', { name: 'Desktops of eng' }).locator('tbody tr');
const actions = (page: import('@playwright/test').Page, desktop: string) =>
	page.locator('main').getByRole('button', { name: `Actions for ${desktop}` });

test("a pool's desktop stops and starts on its claim", async ({ page }) => {
	await setScenario(page, 'desktops');
	await login(page);
	await page.goto('/desktops/team-web/web-prod/pool/eng');
	const bodies: unknown[] = [];
	page.on('request', (r) => {
		if (/\/api\/desktops\/web-prod\/eng-7hq2k\/(start|stop)$/.test(new URL(r.url()).pathname))
			bodies.push(r.postDataJSON());
	});
	await expect(poolRows(page).nth(0)).toContainText('In session');
	await actions(page, 'eng-7hq2k').click();
	await page.getByRole('button', { name: 'Stop', exact: true }).click();
	await expect(page.getByText('Stop requested for eng-7hq2k.')).toBeVisible();
	await expect(poolRows(page).nth(0)).toContainText('Stopped');
	await actions(page, 'eng-7hq2k').click();
	await page.getByRole('button', { name: 'Start', exact: true }).click();
	await expect(page.getByText('Start requested for eng-7hq2k.')).toBeVisible();
	await expect(poolRows(page).nth(0)).toContainText('Ready');
	const sent = { claim: 'eng-alice', uid: 'uid-eng-alice' };
	expect(bodies).toEqual([sent, sent]);
	// The feed names the desktop, and opens its page.
	await page.getByRole('button', { name: /^Recent tasks/ }).click();
	await page.locator('section').getByRole('button', { name: 'Start', exact: true }).click();
	await expect(page).toHaveURL(/\/desktops\/team-web\/web-prod\/desktop\/eng-7hq2k$/);
});

test('release asks first, naming the person and saying the home goes', async ({ page }) => {
	await setScenario(page, 'desktops');
	await login(page);
	await page.goto('/desktops/team-web/web-prod/pool/eng');
	await expect(poolRows(page)).toHaveCount(3);
	await actions(page, 'eng-p4x9m').click();
	await page.getByRole('button', { name: 'Release...' }).click();
	const dialog = page.getByRole('dialog');
	await expect(dialog.getByText('Release eng-p4x9m')).toBeVisible();
	await expect(dialog).toContainText("Deletes bob's claim eng-bob on pool eng.");
	await expect(dialog).toContainText("eng-p4x9m and its home, bob's files, are deleted for good.");
	const confirm = dialog.getByRole('button', { name: 'Release', exact: true });
	await expect(confirm).toBeDisabled();
	await dialog.getByRole('textbox').fill('eng-p4x9m');
	await confirm.click();
	await expect(dialog).toHaveCount(0);
	await expect(page.getByText(/^Released eng-p4x9m: bob's desktop and home/)).toBeVisible();
	await expect(poolRows(page)).toHaveCount(2);
	await expect(poolRows(page).filter({ hasText: 'bob' })).toHaveCount(0);
});

test('a release is held to the claim seen when its menu opened', async ({ page }) => {
	await setScenario(page, 'desktops');
	await login(page);
	await page.goto('/desktops/team-web/web-prod/pool/eng');
	let sent: unknown;
	page.on('request', (r) => {
		if (new URL(r.url()).pathname.endsWith('/eng-p4x9m/release')) sent = r.postDataJSON();
	});
	await actions(page, 'eng-p4x9m').click();
	await page.getByRole('button', { name: 'Release...' }).click();
	const dialog = page.getByRole('dialog');
	await expect(dialog).toContainText("Deletes bob's claim eng-bob on pool eng.");
	// Meanwhile bob's claim is gone and another took its name.
	await setScenario(page, 'desktopsreclaimed');
	await dialog.getByRole('textbox').fill('eng-p4x9m');
	await dialog.getByRole('button', { name: 'Release', exact: true }).click();
	await expect(dialog).toContainText('claim eng-bob no longer holds eng-p4x9m: look at it again');
	expect(sent).toEqual({ claim: 'eng-bob', uid: 'uid-eng-bob' });
	await dialog.getByRole('button', { name: 'Cancel' }).click();
	await expect(poolRows(page)).toHaveCount(3);
});

test('a desktop made one by one is not acted on here', async ({ page }) => {
	await setScenario(page, 'desktops');
	await login(page);
	await page.goto('/desktops/team-web/web-prod/desktop/carol-dev');
	await expect(page.locator('main').getByRole('heading', { level: 1 })).toHaveText('carol-dev');
	await expect(page.locator('main').getByRole('button', { name: /^Actions/ })).toHaveCount(0);
	await page.goto('/desktops/team-web/web-prod');
	const byOne = page.locator('main').getByRole('table', { name: 'Desktops made one by one' });
	await expect(byOne.locator('tbody tr')).toHaveCount(1);
	await expect(byOne.getByRole('button')).toHaveCount(0);
});

test('an act the caller may not make is disabled and says why', async ({ page }) => {
	await setScenario(page, 'desktopsrefused');
	await login(page);
	await page.goto('/desktops/team-web/web-prod/pool/eng');
	await actions(page, 'eng-7hq2k').click();
	const stop = page.getByRole('button', { name: 'Stop', exact: true });
	await expect(stop).toBeDisabled();
	await expect(stop).toHaveAttribute(
		'title',
		"You may not start or stop others' desktops of pool eng.",
	);
	const release = page.getByRole('button', { name: 'Release...' });
	await expect(release).toBeDisabled();
	await expect(release).toHaveAttribute(
		'title',
		"You may not release others' desktops of pool eng.",
	);
	await page.keyboard.press('Escape');
	// A warm desktop has no claim to act on, whatever the caller may do.
	await actions(page, 'eng-w2c8d').click();
	await expect(page.getByRole('button', { name: 'Stop', exact: true })).toHaveAttribute(
		'title',
		'No one holds this desktop: its pool keeps it warm.',
	);
});

test('no act while the console has lost its watch', async ({ page }) => {
	await setScenario(page, 'desktopsstale');
	await login(page);
	await page.goto('/desktops/team-web/web-prod/pool/eng');
	await actions(page, 'eng-7hq2k').click();
	for (const name of ['Stop', 'Release...']) {
		const item = page.getByRole('button', { name, exact: true });
		await expect(item).toBeDisabled();
		await expect(item).toHaveAttribute(
			'title',
			'The console lost its watch on the desktops: acts wait until it is back.',
		);
	}
});

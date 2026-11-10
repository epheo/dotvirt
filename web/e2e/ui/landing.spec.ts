import { expect, login, setScenario, test } from './fx';

// Replication and the Gateway live on their own tabs; each section's landing
// tab carries their state, and a click lands on the row that needs a look.

test('an operator reads replication on the Library and lands on the split disk', async ({
	page,
}) => {
	await setScenario(page, 'replicated');
	await login(page);
	await page.goto('/storage');
	const tile = page
		.locator('main')
		.getByRole('region', { name: 'Needs attention' })
		.locator('> div')
		.first();
	await expect(tile).toContainText('4 of 7');
	await expect(tile).toContainText('disks survive losing a host');
	await expect(tile).toContainText(
		'ledger-export disk: two copies disagree. Pick the one to keep.',
	);
	await tile.getByRole('link', { name: 'Show ledger-export' }).click();
	await expect(page).toHaveURL(/tab=replication&focus=volume%2Fpvc-orphan/);
	const row = page.locator('main tr[aria-current="true"]');
	await expect(row).toContainText('ledger-export');
	await expect(row).toBeInViewport();
});

test('a tenant reads its own disks on the Library and lands on the one that needs help', async ({
	page,
}) => {
	await setScenario(page, 'replicatedtenant');
	await login(page);
	await page.goto('/storage');
	const tile = page
		.locator('main')
		.getByRole('region', { name: 'Needs attention' })
		.locator('> div')
		.first();
	await expect(tile).toContainText('1 of 4');
	await expect(tile).toContainText('ledger-export: This disk cannot recover by itself.');
	await expect(tile).not.toContainText('worker-');
	await tile.getByRole('link', { name: 'Show ledger-export' }).click();
	await expect(page).toHaveURL(/tab=replication&focus=db-prod%2Fledger-export/);
	const row = page.locator('main li[aria-current="true"]');
	await expect(row).toContainText('Needs your platform team');
	await expect(row).toBeInViewport();
});

test('an operator reads the routes on the Map and lands on the refused one', async ({ page }) => {
	await setScenario(page, 'fronted');
	await login(page);
	await page.goto('/networking');
	const tile = page.locator('main').getByRole('region', { name: 'Gateway' });
	await expect(tile).toContainText('3 of 4');
	await expect(tile).toContainText('routes live');
	await expect(tile).toContainText(
		'blog.apps.example.com: Taken. Another route holds this hostname. Pick another.',
	);
	// The controller's text can name another tenant's route: never on the tile.
	await expect(tile).not.toContainText('Taken by route');
	await tile.getByRole('link', { name: 'Show blog.apps.example.com' }).click();
	await expect(page).toHaveURL(
		/tab=gateway&gateway=edge%2Fpublic&route=HTTPRoute%2Fbatch-prod%2Fblog/,
	);
	await expect(
		page
			.locator('main')
			.getByRole('complementary', { name: 'Selected route' })
			.getByText('Another route holds this hostname. Pick another.'),
	).toBeVisible();
});

test('a tenant reads its routes on the Map and lands on the refused one', async ({ page }) => {
	await setScenario(page, 'frontedtenant');
	await login(page);
	await page.goto('/networking');
	const tile = page.locator('main').getByRole('region', { name: 'Gateway' });
	await expect(tile).toContainText('1 of 2');
	await tile.getByRole('link', { name: 'Show blog.apps.example.com' }).click();
	await expect(page).toHaveURL(/tab=gateway&route=HTTPRoute%2Fweb-prod%2Fblog/);
	const row = page.locator('main li[aria-current="true"]');
	await expect(row).toContainText('blog.apps.example.com');
	await expect(row).toContainText('Taken');
});

test('without r-drbd or a GatewayClass the landing tabs carry no such tile', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/storage');
	const main = page.locator('main');
	// The Library's own four tiles, and nothing ahead of them.
	await expect(main.getByRole('region', { name: 'Needs attention' }).locator('> div')).toHaveCount(
		4,
	);
	await page.goto('/networking');
	await expect(main.getByRole('heading', { name: 'Cluster map' })).toBeVisible();
	await expect(main.getByRole('region', { name: 'Gateway' })).toHaveCount(0);
});

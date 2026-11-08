import { expect, login, setScenario, test } from './fx';

// A metrics read the server could not fill answers with a reason and no
// numbers. Every view says the reason in their place: zeros there would read
// as an idle cluster, and "room for 0" as a full one.

const GAP = 'Metrics are not available: the metrics service refused your account.';

test.beforeEach(async ({ page }) => {
	await setScenario(page, 'metricsoff');
	await login(page);
});

test('the scope summary says why it has no capacity or consumers', async ({ page }) => {
	await page.goto('/compute/team-web');
	const main = page.locator('main');
	await expect(main.getByText(GAP)).toBeVisible();
	await expect(main.getByText(/Room for \d+ more/)).toHaveCount(0);
	await expect(main.getByText('Top consumers')).toHaveCount(0);
	await expect(main.getByText(/cores in use/)).toHaveCount(0);
});

test("a VM's usage and charts say why they are empty", async ({ page }) => {
	await page.goto('/vm/web-prod/web-1');
	const main = page.locator('main');
	await expect(main.getByText(GAP)).toBeVisible();
	await expect(main.getByText('Storage (guest)')).toHaveCount(0);
	await page.goto('/vm/web-prod/web-1?tab=monitor');
	await main.getByRole('button', { name: 'Performance' }).click();
	await expect(main.getByText(GAP)).toBeVisible();
	await expect(main.getByText('Loading metrics…')).toHaveCount(0);
});

test('the hosts table drops its metrics columns and says why', async ({ page }) => {
	await page.goto('/hosts');
	const main = page.locator('main');
	await expect(main.getByText(GAP)).toBeVisible();
	await expect(main.getByText('Host balance')).toHaveCount(0);
});

test("a network's top VMs say why traffic is missing", async ({ page }) => {
	await page.goto('/networking/web-prod/web-net');
	const main = page.locator('main');
	await expect(main.getByRole('heading', { name: 'Top VMs' })).toBeVisible();
	await expect(main.getByText(GAP)).toBeVisible();
	await expect(main.getByText('No traffic in the last hour.')).toHaveCount(0);
});

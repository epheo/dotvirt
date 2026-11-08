import { expect, login, setScenario, test } from './fx';

// A failed events read is a failure, said as one, with the retry beside it:
// "No recent events" would claim a quiet cluster the read never saw.

test.beforeEach(async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
});

test("a VM's events lane says the read failed and retries", async ({ page }) => {
	await page.request.post('/__fixture/fail/events/1');
	await page.goto('/vm/web-prod/web-1?tab=monitor');
	const main = page.locator('main');
	await expect(main.getByText(/Events could not be read/)).toBeVisible();
	await expect(main.getByText('No recent events.')).toHaveCount(0);
	await main.getByRole('button', { name: 'Try again' }).click();
	await expect(main.getByText('VirtualMachineInstance started')).toBeVisible();
});

test("a container's events lane says the read failed and retries", async ({ page }) => {
	await page.goto('/compute/team-web');
	const main = page.locator('main');
	await expect(main.getByRole('heading', { level: 1, name: 'team-web' })).toBeVisible();
	await page.request.post('/__fixture/fail/events/1');
	await main.getByRole('link', { name: 'Monitor', exact: true }).click();
	await expect(main.getByText(/Events could not be read/)).toBeVisible();
	await main.getByRole('button', { name: 'Try again' }).click();
	await expect(main.getByText('VirtualMachineInstance started')).toBeVisible();
});

test("the dock's events lane says the read failed and retries", async ({ page }) => {
	await page.request.post('/__fixture/fail/events/1');
	await page.getByRole('button', { name: 'Events', exact: true }).click();
	// The summary page has no events lane of its own: what shows is the dock's.
	const dock = page.locator('section', { has: page.getByRole('button', { name: 'Events' }) });
	await expect(dock.getByText(/Events could not be read/)).toBeVisible();
	await dock.getByRole('button', { name: 'Try again' }).click();
	await expect(dock.getByText('VirtualMachineInstance started')).toBeVisible();
});

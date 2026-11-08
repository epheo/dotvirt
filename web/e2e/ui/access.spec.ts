import { expect, login, setScenario, test } from './fx';

// Authoring fails closed: a namespace the server did not say yes for offers
// no change. A failed check and a denial read differently, so a person knows
// whether to wait or to ask for access.

const UNKNOWN = 'Your access could not be checked. Try again in a moment.';
const DENIED = 'You may not change VMs in db-prod.';

test.beforeEach(async ({ page }) => {
	await setScenario(page, 'access');
	await login(page);
});

test('no verdict: nothing to change, and the check is named as the reason', async ({ page }) => {
	await page.goto('/vm/web-prod/web-1');
	const edit = page.getByRole('button', { name: /Edit Settings/ });
	await expect(edit).toBeDisabled();
	await expect(edit).toHaveAttribute('title', UNKNOWN);
	await expect(page.getByRole('button', { name: /Power off/ })).toBeDisabled();
	await page.goto('/vm/web-prod/web-1?tab=configure');
	await expect(page.locator('main').getByText(UNKNOWN)).toBeVisible();
});

test('an explicit no: nothing to change, and the denial is named', async ({ page }) => {
	await page.goto('/vm/db-prod/db-1');
	const edit = page.getByRole('button', { name: /Edit Settings/ });
	await expect(edit).toBeDisabled();
	await expect(edit).toHaveAttribute('title', DENIED);
	await page.goto('/vm/db-prod/db-1?tab=configure');
	await expect(page.locator('main').getByText(DENIED)).toBeVisible();
});

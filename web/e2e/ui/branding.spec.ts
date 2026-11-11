import { expect, login, setScenario, test } from './fx';

// A provider's dress: its name in the masthead, the title and the sign-in
// screen, its stylesheet after the console's own, its links back to its app.

test("the console wears a provider's branding, sign-in included", async ({ page }) => {
	await setScenario(page, 'branded');
	await page.goto('/');
	await expect(page.getByRole('heading', { name: 'Sign in to Uniwatt Cloud' })).toBeVisible();
	await login(page);
	await page.goto('/compute');
	const header = page.locator('header').first();
	await expect(header.getByRole('link', { name: 'Uniwatt Cloud' })).toBeVisible();
	await expect(page).toHaveTitle('Uniwatt Cloud');
	const account = header.getByRole('link', { name: 'Account' });
	await expect(account).toHaveAttribute('href', 'https://c.example/account');
	await expect(account).toHaveAttribute('rel', 'noopener noreferrer');
	await expect(page.locator('link[rel="stylesheet"][href="/api/branding/theme.css"]')).toHaveCount(
		1,
	);
	await expect
		.poll(() =>
			page.evaluate(() =>
				getComputedStyle(document.documentElement).getPropertyValue('--branded-marker').trim(),
			),
		)
		.toBe('1');
});

test('unbranded, the console is dotvirt', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/compute');
	await expect(page).toHaveTitle('dotvirt');
	await expect(page.locator('link[href="/api/branding/theme.css"]')).toHaveCount(0);
});

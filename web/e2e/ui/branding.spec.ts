import { expect, login, setScenario, test } from './fx';

// A provider's dress: its name throughout the console, sign-in included, its
// stylesheet after the console's own, its links back to its app.

test("the console wears a provider's branding, sign-in included", async ({ page }) => {
	await setScenario(page, 'branded');
	// The served shell carries the branding: the console asks no endpoint for it.
	const asked: string[] = [];
	page.on('request', (r) => {
		if (new URL(r.url()).pathname === '/api/branding') asked.push(r.url());
	});
	await page.goto('/');
	await expect(page.getByRole('heading', { name: 'Sign in to Uniwatt Cloud' })).toBeVisible();
	await login(page);
	await page.goto('/compute');
	const header = page.locator('header').first();
	await expect(header.getByRole('link', { name: 'Uniwatt Cloud' })).toBeVisible();
	await expect(page).toHaveTitle('Uniwatt Cloud');
	// Every icon is the provider's logo: browsers do not simply take the last
	// one declared, so no other may sit beside it.
	await expect(page.locator('link[rel="icon"]')).not.toHaveCount(0);
	await expect(page.locator('link[rel="icon"]:not([href$="/api/branding/logo"])')).toHaveCount(0);
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
	await page.goto('/storage?tab=classes');
	await expect(page.getByText('Managed by the cluster platform, not Uniwatt Cloud')).toBeVisible();
	expect(asked).toEqual([]);
});

test('unbranded, the console is dotvirt', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/compute');
	await expect(page).toHaveTitle('dotvirt');
	await expect(page.locator('link[href="/api/branding/theme.css"]')).toHaveCount(0);
	await expect(page.locator('link[rel="icon"][href$="/api/branding/logo"]')).toHaveCount(0);
	await expect(page.locator('link[rel="icon"]')).not.toHaveCount(0);
});

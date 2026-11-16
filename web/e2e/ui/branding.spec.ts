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
	// Same tab, and the console path is not sent as Referer to the provider's app.
	await expect(account).not.toHaveAttribute('target');
	await expect(account).toHaveAttribute('rel', 'noreferrer');
	await expect(account).toHaveText('Account');
	// A link with an icon shows the icon alone, named by its label, which
	// keyboard focus shows as a tooltip.
	const billing = header.getByRole('link', { name: 'Billing' });
	await expect(billing).toHaveAttribute('href', 'https://c.example/billing');
	await expect(billing).not.toHaveAttribute('target');
	await expect(billing).toHaveAttribute('rel', 'noreferrer');
	await expect(billing.locator('img')).toHaveAttribute('src', '/api/branding/icon/billing.svg');
	await expect(billing.locator('img')).toHaveJSProperty('naturalWidth', 22);
	const tip = billing.getByText('Billing');
	await expect(tip).toBeHidden();
	await billing.hover();
	await expect(tip).toBeVisible();
	// Below the bar, not over its edge.
	const bar = await header.boundingBox();
	const box = await tip.boundingBox();
	expect(box!.y).toBeGreaterThanOrEqual(bar!.y + bar!.height);
	// Escape dismisses it without moving the pointer; it stays dismissed while
	// hovered and comes back once the pointer has left and returned.
	await page.keyboard.press('Escape');
	await expect(tip).toBeHidden();
	await billing.hover({ position: { x: 4, y: 4 } });
	await expect(tip).toBeHidden();
	await page.mouse.move(0, 300);
	await billing.hover();
	await expect(tip).toBeVisible();
	// By keyboard alone, the pointer parked away: Escape hides it, and the
	// next focus of the link shows it again.
	await page.mouse.move(0, 300);
	await billing.focus();
	await expect(tip).toBeVisible();
	await page.keyboard.press('Escape');
	await expect(tip).toBeHidden();
	await page.keyboard.press('Shift+Tab');
	await page.keyboard.press('Tab');
	await expect(billing).toBeFocused();
	await expect(tip).toBeVisible();
	// The link back to this console is the current item: marked, not a link.
	const current = header.locator('[aria-current="page"]');
	await expect(current).toHaveCount(1);
	await expect(current).not.toHaveAttribute('href');
	await expect(current).not.toHaveAttribute('tabindex');
	await expect(current.locator('img')).toHaveAttribute('src', '/api/branding/icon/console.svg');
	await expect(current).toMatchAriaSnapshot('- text: Console');
	// Its underline adds no height: its icon lines up with its neighbours'.
	const mark = await current.locator('img').boundingBox();
	const near = await billing.locator('img').boundingBox();
	expect(mark!.y).toBe(near!.y);
	await expect(header.getByRole('link', { name: 'Console' })).toHaveCount(0);
	await expect(header.getByRole('link')).not.toHaveCount(0);
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

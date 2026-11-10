import { expect, login, setScenario, test } from './fx';

test('an operator follows a route from port to service', async ({ page }) => {
	await setScenario(page, 'fronted');
	await login(page);
	await page.goto('/networking?tab=gateway');
	const main = page.locator('main');
	await expect(main.getByRole('link', { name: /^Gateway/ })).toHaveAttribute(
		'aria-current',
		'page',
	);
	await expect(main.getByRole('heading', { name: 'public' })).toBeVisible();
	await expect(main.getByText('192.0.2.10')).toBeVisible();

	const panel = main.getByRole('complementary', { name: 'Selected route' });
	await main
		.getByRole('region', { name: 'Route map' })
		.getByRole('button', { name: /^shop\.apps/ })
		.click();
	await expect(page).toHaveURL(/route=HTTPRoute/);
	await expect(panel.getByRole('link', { name: 'shop.apps.example.com' })).toBeVisible();
	await expect(panel.getByText('25%')).toBeVisible();
	await expect(panel.getByText('20 requests a second, bursts of 40 on https')).toBeVisible();
});

test('an operator reads why a route is refused', async ({ page }) => {
	await setScenario(page, 'fronted');
	await login(page);
	await page.goto('/networking?tab=gateway');
	const main = page.locator('main');
	await main
		.getByRole('region', { name: 'Route map' })
		.getByRole('button', { name: /^blog\.apps/ })
		.click();
	const panel = main.getByRole('complementary', { name: 'Selected route' });
	await expect(panel.getByText('Another route holds this hostname. Pick another.')).toBeVisible();
	await expect(panel.getByText('Taken by route web-prod/shop')).toBeVisible();
});

test('a second Gateway has its own routes', async ({ page }) => {
	await setScenario(page, 'fronted');
	await login(page);
	await page.goto('/networking?tab=gateway');
	const main = page.locator('main');
	await main.getByRole('tab', { name: 'internal' }).click();
	await expect(page).toHaveURL(/gateway=edge%2Finternal/);
	await expect(main.getByText('10.20.0.5')).toBeVisible();
	await expect(main.getByText('No route uses this Gateway yet.')).toBeVisible();
});

test('a tenant reads its routes and nobody else is named', async ({ page }) => {
	await setScenario(page, 'frontedtenant');
	await login(page);
	await page.goto('/networking?tab=gateway');
	const main = page.locator('main');
	await expect(main.getByRole('heading', { name: 'Your routes' })).toBeVisible();
	await expect(main.getByText('Taken', { exact: true })).toBeVisible();
	await expect(main.getByText('Another route holds this hostname. Pick another.')).toBeVisible();
	await expect(main.getByRole('heading', { name: 'public' })).toHaveCount(0);
	await expect(main).not.toContainText('Taken by route');
});

test('without a GatewayClass there is no Gateway tab', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/networking');
	await expect(page.locator('main').getByRole('link', { name: /^Firewall/ })).toBeVisible();
	await expect(page.locator('main').getByRole('link', { name: /^Gateway/ })).toHaveCount(0);
});

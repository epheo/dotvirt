import type { Page } from '@playwright/test';
import { expect, setScenario, test } from './fx';

// With a sign-in provider ready, the sign-in screen goes straight to it; a user
// already signed in there comes back signed in, to the page they opened. It
// stays put right after a failed sign-in or a sign-out, and the token form is
// the break-glass behind ?token.

const signedIn = (page: Page) =>
	expect(page.locator('aside').getByRole('link', { name: 'Compute' })).toBeVisible();
const providerStarts = async (page: Page): Promise<number> =>
	(await (await page.request.get('/__fixture/provider-starts')).json()).starts;

test('a ready provider signs a user in without a click', async ({ page }) => {
	await setScenario(page, 'oidc');
	await page.goto('/');
	await signedIn(page);
	expect(await providerStarts(page)).toBe(1);
	await expect(page.locator('textarea')).toHaveCount(0);
});

test('a deep link survives the provider round trip', async ({ page }) => {
	await setScenario(page, 'ssoready');
	await page.goto('/storage?x=1');
	await signedIn(page);
	await expect(page).toHaveURL(/\/storage\?x=1$/);
});

test('a failed sign-in shows one button, never a redirect', async ({ page }) => {
	await setScenario(page, 'oidc');
	await page.goto('/?sso_error=1');
	await expect(page.getByText('Sign-in with Kanidm failed. Try again.')).toBeVisible();
	// / lands on the home section, which the button returns to.
	await expect(page.getByRole('link', { name: 'Sign in with Kanidm' })).toHaveAttribute(
		'href',
		'/api/auth/oidc?return=%2Fcompute',
	);
	await expect(page.locator('textarea')).toHaveCount(0);
	expect(await providerStarts(page)).toBe(0);
});

test('a sign-out lands on a button, not back in through the provider', async ({ page }) => {
	await setScenario(page, 'oidc');
	await page.goto('/');
	await signedIn(page);
	await page.getByRole('button', { name: 'admin' }).click();
	await page.getByRole('button', { name: 'Sign out' }).click();
	await expect(page).toHaveURL(/[?&]signed_out($|&)/);
	await expect(page.getByText('You are signed out.')).toBeVisible();
	await expect(page.locator('textarea')).toHaveCount(0);
	expect(await providerStarts(page)).toBe(1);
	// The button signs back in.
	await page.getByRole('link', { name: 'Sign in with Kanidm' }).click();
	await signedIn(page);
});

test('a provider that leaves no session is not retried in a loop', async ({ page }) => {
	await setScenario(page, 'oidcsessionless');
	await page.goto('/compute');
	await expect(page.getByText(/did not keep you signed in/)).toBeVisible();
	expect(await providerStarts(page)).toBe(1);
	await expect(page.getByRole('link', { name: 'Sign in with Kanidm' })).toHaveAttribute(
		'href',
		'/api/auth/oidc?return=%2Fcompute',
	);
});

test('no provider ready: the token form', async ({ page }) => {
	await setScenario(page, 'base');
	await page.goto('/');
	await expect(page.locator('textarea')).toBeVisible();
	expect(await providerStarts(page)).toBe(0);
});

test('?token shows the token form even with a provider ready', async ({ page }) => {
	await setScenario(page, 'oidc');
	await page.goto('/?token');
	await expect(page.locator('textarea')).toBeVisible();
	await expect(page.getByRole('link', { name: 'Sign in with Kanidm' })).toBeVisible();
	expect(await providerStarts(page)).toBe(0);
	await page.locator('textarea').fill('fixture-token');
	await page.getByRole('button', { name: 'Sign in with token' }).click();
	await signedIn(page);
});

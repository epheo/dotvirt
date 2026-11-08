import type { Page } from '@playwright/test';
import { expect, login, setScenario, test } from './fx';

// A sign-out that did not reach the server leaves the session live: the UI
// says so and keeps it, rather than showing a login screen a reload would
// walk straight past on a shared browser.

const FAILED = 'Sign-out did not complete. Your session is still active.';
const signedIn = (page: Page) =>
	expect(page.locator('aside').getByRole('link', { name: 'Compute' })).toBeVisible();

async function signOut(page: Page) {
	await page.getByRole('button', { name: 'admin' }).click();
	await page.getByRole('button', { name: 'Sign out' }).click();
}

test('a failed sign-out keeps the session and says so', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.request.post('/__fixture/fail/logout/1');
	await signOut(page);
	await expect(page.getByText(FAILED)).toBeVisible();
	await signedIn(page);
	// The session really is live: a reload lands signed in, not on Login.
	await page.reload();
	await signedIn(page);
});

test('Retry finishes a failed sign-out', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.request.post('/__fixture/fail/logout/1');
	await signOut(page);
	await page.getByRole('button', { name: 'Retry sign-out' }).click();
	await expect(page.locator('textarea')).toBeVisible();
	await page.reload();
	await expect(page.locator('textarea')).toBeVisible();
});

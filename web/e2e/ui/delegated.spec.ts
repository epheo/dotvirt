import type { Page } from '@playwright/test';
import { expect, login, setScenario, test } from './fx';

// Pull requests through each user's own forge account: the first sign-in of a
// console session connects it on its own, once; an abandoned connect leaves
// the console usable and is not restarted that session; the user menu
// connects and disconnects it.

const signedIn = (page: Page) =>
	expect(page.locator('aside').getByRole('link', { name: 'Compute' })).toBeVisible();
const connects = async (page: Page): Promise<number> =>
	(await (await page.request.get('/__fixture/forge-connects')).json()).connects;
const userMenu = (page: Page) => page.getByRole('button', { name: 'admin' });

// signInAt signs in with the fixture token on the sign-in screen of path, so
// the connect starts from there.
async function signInAt(page: Page, path: string) {
	await page.goto(path);
	await page.fill('textarea', 'fixture-token');
	await page.click('button[type="submit"]');
}

test('the first sign-in connects the forge account once, back on the same page', async ({
	page,
}) => {
	await setScenario(page, 'delegated');
	await signInAt(page, '/storage?x=1');
	await expect.poll(() => connects(page)).toBe(1);
	await expect(page).toHaveURL(/\/storage\?x=1$/);
	await signedIn(page);
	await userMenu(page).click();
	await expect(page.getByText('Connected as admin-forge')).toBeVisible();
	// A reload is the same console session: no second connect.
	await page.keyboard.press('Escape');
	await page.reload();
	await signedIn(page);
	expect(await connects(page)).toBe(1);
});

test('an abandoned connect leaves the console usable and is not restarted', async ({ page }) => {
	await setScenario(page, 'delegatedabandon');
	await signInAt(page, '/compute');
	await expect(page).toHaveURL(/\/__fixture\/forge\/authorize$/);
	await expect(page.getByRole('heading', { name: 'Authorize the console?' })).toBeVisible();
	// The user walks away from the forge's page and back to the console.
	await page.goto('/compute');
	await signedIn(page);
	// Even past the tab's repeat guard, the session already asked once.
	await page.evaluate(() => sessionStorage.removeItem('dotvirt.forge-connect-at'));
	await page.reload();
	await signedIn(page);
	expect(await connects(page)).toBe(1);
	await userMenu(page).click();
	await expect(page.getByRole('button', { name: 'Connect git account' })).toBeVisible();
});

test('the user menu disconnects and reconnects the forge account', async ({ page }) => {
	await setScenario(page, 'delegated');
	await signInAt(page, '/compute');
	await expect.poll(() => connects(page)).toBe(1);
	await signedIn(page);
	await userMenu(page).click();
	await page.getByRole('button', { name: 'Disconnect git account' }).click();
	await userMenu(page).click();
	await expect(page.getByRole('button', { name: 'Connect git account' })).toBeVisible();
	await expect(page.getByText(/Connected as/)).toHaveCount(0);
	// Disconnecting does not reconnect on its own.
	await page.reload();
	await signedIn(page);
	expect(await connects(page)).toBe(1);
	await userMenu(page).click();
	await page.getByRole('button', { name: 'Connect git account' }).click();
	await signedIn(page);
	expect(await connects(page)).toBe(2);
	await userMenu(page).click();
	await expect(page.getByText('Connected as admin-forge')).toBeVisible();
});

test('a sign-out forgets the connection; the next sign-in asks again', async ({ page }) => {
	await setScenario(page, 'delegatedabandon');
	await signInAt(page, '/compute');
	await expect(page).toHaveURL(/\/__fixture\/forge\/authorize$/);
	await page.goto('/compute');
	await signedIn(page);
	await userMenu(page).click();
	await page.getByRole('button', { name: 'Sign out' }).click();
	await expect(page).toHaveURL(/[?&]signed_out($|&)/);
	// A later sign-in: past the tab's one-minute repeat guard.
	await page.evaluate(() => sessionStorage.removeItem('dotvirt.forge-connect-at'));
	await page.fill('textarea', 'fixture-token');
	await page.click('button[type="submit"]');
	await expect(page).toHaveURL(/\/__fixture\/forge\/authorize$/);
	expect(await connects(page)).toBe(2);
});

test('a second automatic connect within a minute in a tab is held', async ({ page }) => {
	await setScenario(page, 'delegatedabandon');
	await signInAt(page, '/compute');
	await expect(page).toHaveURL(/\/__fixture\/forge\/authorize$/);
	await page.goto('/compute');
	await signedIn(page);
	await userMenu(page).click();
	await page.getByRole('button', { name: 'Sign out' }).click();
	await expect(page).toHaveURL(/[?&]signed_out($|&)/);
	await page.fill('textarea', 'fixture-token');
	await page.click('button[type="submit"]');
	await signedIn(page);
	expect(await connects(page)).toBe(1);
});

test('a tab that cannot record its connects starts none on its own', async ({ page }) => {
	await page.addInitScript(() => {
		Object.defineProperty(window, 'sessionStorage', {
			get() {
				throw new DOMException('blocked', 'SecurityError');
			},
		});
	});
	await setScenario(page, 'delegated');
	await signInAt(page, '/compute');
	await signedIn(page);
	await page.waitForTimeout(1000);
	expect(await connects(page)).toBe(0);
	await userMenu(page).click();
	await expect(page.getByRole('button', { name: 'Connect git account' })).toBeVisible();
});

test('without delegation the console shows no git account and starts no connect', async ({
	page,
}) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/compute');
	await signedIn(page);
	await userMenu(page).click();
	await expect(page.getByText('Git account')).toHaveCount(0);
	expect(await connects(page)).toBe(0);
});

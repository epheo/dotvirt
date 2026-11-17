import type { Page } from '@playwright/test';
import { expect, login, openTray, openVM, setScenario, stageChange, test } from './fx';

// Pull requests through each user's own forge account: the first sign-in of a
// console session connects it on its own, once; an abandoned connect leaves
// the console usable and is not restarted that session; the user menu
// connects and disconnects it. A write refused for want of a connection, or
// of write on the repo, says why and offers the one click forward.

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

test('a forge that does not answer gets no automatic connect', async ({ page }) => {
	await setScenario(page, 'delegateddown');
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
	const reads: string[] = [];
	page.on('request', (r) => {
		if (r.url().includes('/api/forge/')) reads.push(r.url());
	});
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/compute');
	await signedIn(page);
	await userMenu(page).click();
	await expect(page.getByText('Git account')).toHaveCount(0);
	expect(await connects(page)).toBe(0);
	// A default install reads nothing of the forge connection.
	expect(reads).toEqual([]);
});

// stageMemory stages a memory change on web-1 (team-web).
async function stageMemory(page: Page) {
	await openVM(page, 'web-1');
	await page.getByRole('button', { name: /Edit Settings/ }).click();
	await page.getByRole('dialog').getByLabel('Memory').fill('8Gi');
	await stageChange(page);
}

// disconnected signs in on a delegated install, lets the first connect
// complete, then drops it from the user menu: no connect restarts on its own.
async function disconnected(page: Page) {
	await setScenario(page, 'delegated');
	await signInAt(page, '/compute');
	await expect.poll(() => connects(page)).toBe(1);
	await signedIn(page);
	await userMenu(page).click();
	await page.getByRole('button', { name: 'Disconnect git account' }).click();
}

test('proposing without a connection says why and connects in one click, back on the page', async ({
	page,
}) => {
	await disconnected(page);
	await stageMemory(page);
	const at = new URL(page.url()).pathname;

	await (await openTray(page)).getByRole('button', { name: 'Review and propose' }).click();
	const sheet = page.getByRole('dialog');
	await sheet.getByRole('button', { name: 'Open pull request' }).click();
	const refused = sheet.getByRole('alert');
	await expect(refused).toContainText('Not proposed yet.');
	await expect(refused).toContainText('your git account is not connected to this console session');
	// Said once, where the button is: the refusal starts no connect itself.
	await page.waitForTimeout(500);
	expect(await connects(page)).toBe(1);

	await refused.getByRole('button', { name: 'Connect git account' }).click();
	await expect.poll(() => connects(page)).toBe(2);
	await expect(page).toHaveURL(new RegExp(`${at}$`));
	await signedIn(page);
	// The draft waited; now it goes to the forge as the user.
	await (await openTray(page)).getByRole('button', { name: 'Review and propose' }).click();
	await page.getByRole('dialog').getByRole('button', { name: 'Open pull request' }).click();
	await expect(page.getByText(/PR #77 opened in team-web/)).toBeVisible();
});

test('an undo without a connection offers the connect in its dialog', async ({ page }) => {
	await disconnected(page);
	await page.goto('/changes');
	const main = page.locator('main');
	await main.getByRole('link', { name: /^History/ }).click();
	await main.getByRole('button', { name: /^team-web$/ }).click();
	await main.getByRole('button', { name: /web-2: add data disk/ }).click();
	await main.getByRole('button', { name: 'Undo this change' }).click();
	const dialog = page.getByRole('dialog', { name: /Undo this change/ });
	await dialog.getByRole('button', { name: 'Open pull request' }).click();
	await expect(dialog.getByText(/your git account is not connected/)).toBeVisible();
	await dialog.getByRole('button', { name: 'Connect git account' }).click();
	await expect.poll(() => connects(page)).toBe(2);
	await signedIn(page);
});

test('a user without write on the repo is told what to ask for, and where', async ({ page }) => {
	await setScenario(page, 'delegatednowrite');
	await signInAt(page, '/compute');
	await expect.poll(() => connects(page)).toBe(1);
	await signedIn(page);
	await stageMemory(page);

	await (await openTray(page)).getByRole('link', { name: 'Open in Changes' }).click();
	const main = page.locator('main');
	await main.getByRole('button', { name: 'Propose pull request' }).click();
	await expect(
		main.getByText(
			/ask the owners of dotvirt\/team-web in the forge to give admin-forge write access/,
		),
	).toBeVisible();
	await expect(main.getByRole('link', { name: 'Open the repo in the forge' })).toHaveAttribute(
		'href',
		'https://forge.example/dotvirt/team-web.git',
	);
	// Connecting again would change nothing: no connect is offered.
	await expect(main.getByRole('button', { name: 'Connect git account' })).toHaveCount(0);
	expect(await connects(page)).toBe(1);
});

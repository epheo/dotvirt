import { expect, type Page } from '@playwright/test';

// The steps both suites take, live and fixture-backed: the token login, the
// walk to one VM's detail page, and unfolding the change tray.

// signIn pastes a token and waits for the shell: the section links in the
// sidebar exist only once authenticated.
export async function signIn(page: Page, token: string) {
	await page.goto('/');
	await page.waitForSelector('textarea');
	await page.fill('textarea', token);
	await page.click('button[type="submit"]');
	await expect(page.locator('aside').getByRole('link', { name: 'Compute' })).toBeVisible();
}

// openVM switches to the VMs tab and opens a VM's detail route through its
// name link (a plain row click opens the side peek); the first row when no
// name is given.
export async function openVM(page: Page, name?: string) {
	await page.locator('main').getByRole('link', { name: 'VMs', exact: true }).click();
	const rows = page.locator('main tbody tr', name ? { hasText: name } : undefined);
	const row = rows.first();
	await expect(row).toBeVisible();
	const link = name ? row.getByRole('link', { name, exact: true }) : row.getByRole('link').first();
	await link.click();
	await expect(page.getByRole('button', { name: /Edit Settings/ })).toBeVisible();
}

// openTray unfolds the change tray, a pill until the user asks, and returns it.
export async function openTray(page: Page) {
	const tray = page.getByRole('complementary', { name: 'Changes in flight' });
	await tray.waitFor();
	if (!(await tray.getByRole('button', { name: 'Fold' }).isVisible()))
		await tray.getByRole('button').first().click();
	await expect(tray.getByRole('button', { name: 'Fold' })).toBeVisible();
	return tray;
}

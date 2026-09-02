import AxeBuilder from '@axe-core/playwright';
import { expect, login, openVM, setScenario, test } from './fx';

// Axe pass over the main surfaces (base scenario), color-contrast included —
// the ink/accent tokens are tuned to clear 4.5:1 on every ground, in both
// themes. Serious/critical violations fail; the rest land in the report.

async function checkA11y(page: import('@playwright/test').Page, surface: string) {
	const results = await new AxeBuilder({ page }).analyze();
	const blocking = results.violations.filter(
		(v) => v.impact === 'serious' || v.impact === 'critical',
	);
	expect(
		blocking.map((v) => `${surface}: [${v.impact}] ${v.id} — ${v.help} (${v.nodes.length} nodes)`),
	).toEqual([]);
}

test('login screen is accessible', async ({ page }) => {
	await setScenario(page, 'base');
	await page.goto('/');
	await page.waitForSelector('textarea');
	await checkA11y(page, 'login');
});

test('inventory surfaces are accessible', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await checkA11y(page, 'compute summary');
	await page.emulateMedia({ colorScheme: 'dark' });
	await checkA11y(page, 'compute summary (dark)');
	await page.emulateMedia({ colorScheme: 'light' });

	await page.locator('main').getByRole('link', { name: 'VMs', exact: true }).click();
	await checkA11y(page, 'vm table');

	await openVM(page, 'web-1');
	await checkA11y(page, 'vm detail');
});

test('networking and security surfaces are accessible', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/networking');
	await expect(page.locator('main').getByText('Provider Gateway').first()).toBeVisible();
	await checkA11y(page, 'topology');

	await page.goto('/networking/security');
	await checkA11y(page, 'security');
});

test('a VM opens by keyboard alone', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.locator('main').getByRole('link', { name: 'VMs', exact: true }).click();
	// The name cell is a real link, so Tab reaches it and Enter opens it — the
	// row's mouse onclick alone left keyboard users no way in.
	const link = page.locator('main tbody').getByRole('link', { name: 'web-1' });
	await link.focus();
	await page.keyboard.press('Enter');
	await expect(page).toHaveURL(/\/vm\/web-prod\/web-1/);
	await expect(page.getByRole('button', { name: /Edit Settings/ })).toBeVisible();
});

test('hosts, storage, catalog and trouble states are accessible', async ({ page }) => {
	await setScenario(page, 'drift');
	await login(page);
	for (const path of ['/hosts', '/storage', '/catalog']) {
		await page.goto(path);
		await page.waitForTimeout(300);
		await checkA11y(page, path);
	}
	await page.goto('/compute');
	await page.locator('button[title^="Issues"]').click();
	await checkA11y(page, 'issues bell (drift)');
	await page.keyboard.press('Escape');
	await page.getByRole('link', { name: /Review changes/ }).click();
	await page.waitForTimeout(300);
	await checkA11y(page, 'changes route (drift)');
});

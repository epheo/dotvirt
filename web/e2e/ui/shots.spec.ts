import { expect, login, openVM, setScenario, test } from './fx';

// Screenshot capture, not assertion: every key view in both themes, per
// scenario, written to e2e/screenshots/ and uploaded as a CI artifact for a
// human pass. Cheap to keep honest — the states are scripted, so the same
// screenshot always shows the same state.

const dir = 'e2e/screenshots';

async function shot(page: import('@playwright/test').Page, name: string) {
	await page.waitForTimeout(300); // let charts/skeletons settle
	await page.screenshot({ path: `${dir}/${name}.png`, fullPage: false });
}

test('base views, light and dark', async ({ page }) => {
	await setScenario(page, 'base');
	await page.goto('/');
	await page.waitForSelector('textarea');
	await shot(page, 'login');

	await login(page);
	await shot(page, 'compute-summary');

	await page.locator('main').getByRole('link', { name: 'VMs', exact: true }).click();
	await shot(page, 'vm-table');

	await page.locator('main tbody tr', { hasText: 'web-1' }).first().click();
	await shot(page, 'vm-peek');
	await page.keyboard.press('Escape');

	await openVM(page, 'web-1');
	await shot(page, 'vm-detail');

	await page.goto('/networking');
	await expect(page.locator('main').getByText('Provider Gateway').first()).toBeVisible();
	await shot(page, 'networking-topology');

	await page.goto('/networking/security');
	await shot(page, 'security');

	await page.goto('/hosts');
	await shot(page, 'hosts');

	await page.emulateMedia({ colorScheme: 'dark' });
	await page.goto('/compute');
	await expect(page.locator('aside').getByText('team-web')).toBeVisible();
	await shot(page, 'compute-summary-dark');
	await page.emulateMedia({ colorScheme: 'light' });
});

test('trouble states', async ({ page }) => {
	await setScenario(page, 'drift');
	await login(page);
	await page.locator('button[title^="Issues"]').click();
	await shot(page, 'drift-issues-bell');
	await page.keyboard.press('Escape');
	await page.getByRole('link', { name: /Review changes/ }).click();
	await shot(page, 'drift-changes-prune-warning');

	await setScenario(page, 'degraded');
	await page.goto('/compute');
	await expect(page.getByText(/live VM state unavailable/)).toBeVisible();
	await shot(page, 'degraded-compute');

	await setScenario(page, 'large');
	await page.goto('/compute');
	await expect(page.locator('aside').getByText('tenant-11', { exact: true })).toBeVisible();
	await shot(page, 'large-tree');
});

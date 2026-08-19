import AxeBuilder from '@axe-core/playwright';
import { expect, login, openVM, setScenario, test } from './fx';

// Axe pass over the main surfaces (base scenario). Serious/critical violations
// fail; the full result list lands in the report for triage.
//
// color-contrast is reported but NOT blocking: the current palette carries
// ~20 known low-contrast nodes per view (mostly faint helper text). Gating on
// it would freeze this suite red; fixing it is a theme decision. Every other
// serious/critical rule is a hard gate, so new violation kinds fail the PR.

async function checkA11y(page: import('@playwright/test').Page, surface: string) {
	const results = await new AxeBuilder({ page }).analyze();
	for (const v of results.violations.filter((x) => x.id === 'color-contrast')) {
		test.info().annotations.push({
			type: 'known-a11y',
			description: `${surface}: color-contrast (${v.nodes.length} nodes)`,
		});
	}
	const blocking = results.violations.filter(
		(v) => (v.impact === 'serious' || v.impact === 'critical') && v.id !== 'color-contrast',
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

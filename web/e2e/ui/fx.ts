import { test as base, expect, type Page } from '@playwright/test';

// Shared plumbing for the hermetic UI suite: scenario switching against the
// fixture server's control plane, the login shortcut, and a console gate that
// fails any test whose page throws or logs an unexpected error — silent
// breakage is exactly what this suite exists to catch.

export { expect };

export const test = base.extend<{ consoleGuard: string[] }>({
	// Auto-collected page errors, asserted empty when the test ends. Expected
	// transport noise is filtered: scenario states deliberately answer 503 on
	// degraded planes, and the reconnect tests kill WebSockets on purpose — the
	// UI's *handling* of those is what the specs assert.
	consoleGuard: [
		async ({ page }, use) => {
			const errors: string[] = [];
			page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`));
			page.on('console', (m) => {
				if (m.type() !== 'error') return;
				const text = m.text();
				if (/Failed to load resource|the server responded with a status/.test(text)) return;
				if (/WebSocket connection to .* failed/.test(text)) return;
				errors.push(`console.error: ${text}`);
			});
			await use(errors);
			expect(errors, 'the page logged errors during the test').toEqual([]);
		},
		{ auto: true },
	],
});

// setScenario flips the fixture server's state; call BEFORE login/goto so the
// first inventory frame already carries the scenario.
export async function setScenario(page: Page, name: string) {
	const res = await page.request.post(`/__fixture/scenario/${name}`);
	expect(res.ok(), `switch to scenario ${name}`).toBeTruthy();
}

// login signs in with the fixture token and waits for the app shell.
export async function login(page: Page) {
	await page.goto('/');
	await page.fill('textarea', 'fixture-token');
	await page.click('button[type="submit"]');
	await expect(page.locator('aside').getByRole('link', { name: 'Compute' })).toBeVisible();
}

export async function openVM(page: Page, name: string) {
	await page.locator('main').getByRole('link', { name: 'VMs', exact: true }).click();
	// The name link: a plain row click opens the side peek, not the detail page.
	await page
		.locator('main tbody tr', { hasText: name })
		.first()
		.getByRole('link', { name, exact: true })
		.click();
	await expect(page.getByRole('button', { name: /Edit Settings/ })).toBeVisible();
}

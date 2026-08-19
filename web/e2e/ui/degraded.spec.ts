import { expect, login, setScenario, test } from './fx';

// Degraded planes (scenario: degraded): forge down, live watch failing,
// metrics and alerts absent. The contract everywhere is the same — say what is
// degraded where the user already is, keep the rest working, never crash.
// Drawn from real incidents (forge 503 flapping, stale SA token).

test.beforeEach(async ({ page }) => {
	await setScenario(page, 'degraded');
	await login(page);
});

test('live-plane outage is a banner, not silently stopped VMs', async ({ page }) => {
	await expect(page.getByText(/live VM state unavailable/)).toBeVisible();
	// The git plane still renders: the VM exists, without invented live facts.
	const main = page.locator('main');
	await main.getByRole('link', { name: 'VMs', exact: true }).click();
	await expect(main.getByText('db-1')).toBeVisible();
});

test('a broken repo is a named standing issue', async ({ page }) => {
	await page.locator('button[title^="Issues"]').click();
	await expect(page.getByText('Repository problem')).toBeVisible();
	// The cause rides the row's tooltip; the row itself links to the project.
	await expect(page.locator('button[title*="503 Service Unavailable"]')).toBeVisible();
});

test('absent alerts feed says so instead of showing an empty list', async ({ page }) => {
	await page.getByRole('button', { name: /Alarms/ }).click();
	await expect(page.getByText(/alerts feed unavailable/)).toBeVisible();
});

test('metrics-less install renders Monitor without a crash', async ({ page }) => {
	const main = page.locator('main');
	await main.getByRole('link', { name: 'Monitor', exact: true }).click();
	// Whatever the panels say, the page must hold: the console guard fixture
	// fails this test on any page error; the 503s themselves are expected.
	await expect(main.getByRole('link', { name: 'Summary', exact: true })).toBeVisible();
});

test('platform authoring gates follow the caps, not hardcoded roles', async ({ page }) => {
	// nmstate absent + no platform caps: the fabric affordances hide rather
	// than render empty panels.
	await page.locator('aside').getByRole('link', { name: 'Networking' }).click();
	await expect(page.locator('main').getByText('Provider Gateway').first()).toBeVisible();
});

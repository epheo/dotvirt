import { expect, login, setScenario, test } from './fx';

// Session resilience. Two real incidents behind these specs: a backend deploy
// used to bounce every signed-in user to login (a WS close before open was
// assumed to be a 401), and a backend/cluster outage at login read as "token
// rejected", sending users to re-mint perfectly good tokens.

test('a dropped stream reconnects without signing the user out', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);

	await page.request.post('/__fixture/ws-drop');
	// Still signed in: no login form reappears.
	await expect(page.locator('textarea')).toHaveCount(0);

	// The reconnected socket receives fresh frames: flip the scenario and the
	// tree repaints with drift content, no reload, no re-login.
	await setScenario(page, 'drift');
	await expect(page.locator('aside').getByText('db-prod')).toBeVisible();
	await expect(page.locator('textarea')).toHaveCount(0);
});

test('handshake failures during a deploy retry instead of logging out', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);

	// The next two WS handshakes die before the socket opens — the ambiguous
	// close the client must resolve by probing the session, not by assuming 401.
	await page.request.post('/__fixture/ws-refuse/2');
	await page.request.post('/__fixture/ws-drop');

	// Backoff (0.5s, 1s) then a clean reconnect; the session survives.
	await setScenario(page, 'large');
	await expect(page.locator('aside').getByText('tenant-0', { exact: true })).toBeVisible({
		timeout: 15_000,
	});
	await expect(page.locator('textarea')).toHaveCount(0);
});

test('an unreachable backend at login is not blamed on the token', async ({ page }) => {
	await setScenario(page, 'base');
	await page.goto('/');
	await page.request.post('/__fixture/login-status/503');

	await page.fill('textarea', 'perfectly-good-token');
	await page.click('button[type="submit"]');
	await expect(page.getByText(/backend or cluster is unreachable/)).toBeVisible();
	await expect(page.getByText(/token was rejected/)).toHaveCount(0);

	// A genuinely bad token still gets the token message.
	await page.fill('textarea', 'bad-token');
	await page.click('button[type="submit"]');
	await expect(page.getByText(/token was rejected/)).toBeVisible();

	// And the same token works once the backend is back.
	await page.fill('textarea', 'perfectly-good-token');
	await page.click('button[type="submit"]');
	await expect(page.locator('aside').getByRole('link', { name: 'Compute' })).toBeVisible();
});

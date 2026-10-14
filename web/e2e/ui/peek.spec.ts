import { expect, login, setScenario, test } from './fx';

// The side peek: a row click inspects in place, Enter opens the full page,
// Esc closes - the list never leaves the screen.

test('row click peeks; Enter expands; Esc closes', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.locator('main').getByRole('link', { name: 'VMs', exact: true }).click();
	await page.locator('main tbody tr', { hasText: 'web-1' }).first().click();

	const peek = page.getByRole('complementary', { name: 'VM inspector' });
	await expect(peek).toBeVisible();
	// Live facts off the stream, no navigation.
	await expect(peek.getByText('worker-1')).toBeVisible();
	await expect(peek.getByText('10.128.0.10')).toBeVisible();
	await expect(page).toHaveURL(/peek=/);
	await expect(page).not.toHaveURL(/\/vm\//);

	// The table stays on screen beside the peek.
	await expect(page.locator('main tbody tr', { hasText: 'web-2' })).toBeVisible();

	await page.keyboard.press('Escape');
	await expect(peek).toBeHidden();
	await expect(page).not.toHaveURL(/peek=/);

	await page.locator('main tbody tr', { hasText: 'web-1' }).first().click();
	await page.keyboard.press('Enter');
	await expect(page).toHaveURL(/\/vm\/web-prod\/web-1/);
	await expect(page.getByRole('button', { name: /Edit Settings/ })).toBeVisible();
});

test('rows move by arrow keys; Enter peeks then expands; Space selects', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.locator('main').getByRole('link', { name: 'VMs', exact: true }).click();
	const rows = page.locator('main tbody tr');
	await rows.first().focus();
	await page.keyboard.press('ArrowDown');
	const second = rows.nth(1);
	await expect(second).toBeFocused();
	const name = (await second.locator('a').textContent())!.trim();

	await page.keyboard.press('Space');
	await expect(second).toHaveAttribute('aria-selected', 'true');

	await page.keyboard.press('Enter');
	const peek = page.getByRole('complementary', { name: 'VM inspector' });
	await expect(peek.getByText(name, { exact: true }).first()).toBeVisible();
	await expect(page).not.toHaveURL(/\/vm\//);

	await page.keyboard.press('Enter');
	await expect(page).toHaveURL(new RegExp(`/vm/[^/]+/${name}`));
});

test('a peeked VM that leaves the scope closes its peek', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.locator('main').getByRole('link', { name: 'VMs', exact: true }).click();
	await page.locator('main tbody tr', { hasText: 'web-1' }).first().click();
	await expect(page.getByRole('complementary', { name: 'VM inspector' })).toBeVisible();

	// Narrow the scope to a project that does not hold the peeked VM.
	await page.goto('/compute/team-db?tab=vms&peek=web-prod%2Fweb-1');
	await expect(page.getByRole('complementary', { name: 'VM inspector' })).toBeHidden();
});

test('palette verb dispatches a runtime action', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);

	await page.keyboard.press('Control+k');
	await page.getByLabel('Search inventory').fill('restart web-1');
	// Scope to the masthead: the task dock can carry a same-named feed row.
	const hit = page.locator('header').getByRole('button', { name: /Restart web-1/ });
	await expect(hit).toBeVisible();
	await hit.click();
	// The runtime op ran (fixture 204) and reported through the one toast path.
	await expect(page.getByText('Restart requested for web-1.')).toBeVisible();
});

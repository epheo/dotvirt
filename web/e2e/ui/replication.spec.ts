import { expect, login, setScenario, test } from './fx';

test('an operator sees every copy and rehearses losing a host', async ({ page }) => {
	await setScenario(page, 'replicated');
	await login(page);
	await page.goto('/storage?tab=replication');
	const main = page.locator('main');
	await expect(main.getByRole('heading', { name: 'Where every copy lives' })).toBeVisible();
	await expect(main.getByText('Two copies disagree', { exact: true })).toBeVisible();
	await expect(main.getByText(/worker-3 catching up, 64%/)).toBeVisible();

	await main.getByRole('button', { name: /^worker-1/ }).click();
	await expect(page).toHaveURL(/down=worker-1/);
	await expect(main.getByRole('heading', { name: 'If worker-1 goes down' })).toBeVisible();
	await expect(main.getByText('Rehearsal, nothing changes on the cluster')).toBeVisible();
	await expect(main.getByRole('button', { name: /^worker-1/ })).toHaveAttribute(
		'aria-pressed',
		'true',
	);

	await main.getByRole('button', { name: 'End rehearsal' }).click();
	await expect(page).not.toHaveURL(/down=/);
	await expect(main.getByRole('heading', { name: 'Protection' })).toBeVisible();
});

test('a rehearsal is a link', async ({ page }) => {
	await setScenario(page, 'replicated');
	await login(page);
	await page.goto('/storage?tab=replication&down=worker-3');
	await expect(
		page.locator('main').getByRole('heading', { name: 'If worker-3 goes down' }),
	).toBeVisible();
});

test('a tenant reads its own disks and no host', async ({ page }) => {
	await setScenario(page, 'replicatedtenant');
	await login(page);
	await page.goto('/storage?tab=replication');
	const main = page.locator('main');
	await expect(main.getByRole('heading', { name: 'Your replicated disks' })).toBeVisible();
	await expect(main.getByText('Survives losing any one host.')).toBeVisible();
	await expect(main.getByText('Needs your platform team')).toBeVisible();
	await expect(main.getByRole('heading', { name: 'Where every copy lives' })).toHaveCount(0);
	await expect(main.locator('section', { hasText: 'Your replicated disks' })).not.toContainText(
		'worker-',
	);
});

test('a project shows its claims that no VM holds', async ({ page }) => {
	await setScenario(page, 'replicated');
	await login(page);
	await page.goto('/storage?tab=replication&project=team-db');
	const main = page.locator('main');
	await expect(main.getByText('ledger-export', { exact: true })).toBeVisible();
	await expect(main.getByRole('link', { name: 'web-1' })).toHaveCount(0);

	await setScenario(page, 'replicatedtenant');
	await page.goto('/storage?tab=replication&project=team-db');
	await expect(main.getByText('ledger-export', { exact: true })).toBeVisible();
	await expect(main.getByRole('link', { name: 'web-1' })).toHaveCount(0);
});

test('without r-drbd there is no Replication tab', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/storage');
	await expect(page.locator('main').getByRole('link', { name: /^Classes \d+$/ })).toBeVisible();
	await expect(page.locator('main').getByRole('link', { name: 'Replication' })).toHaveCount(0);
});

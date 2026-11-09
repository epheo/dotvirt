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

test('keeping one copy of a split disk means typing its name', async ({ page }) => {
	await setScenario(page, 'replicated');
	await login(page);
	await page.goto('/storage?tab=replication');
	const main = page.locator('main');
	await main.getByRole('button', { name: /^Keep one copy of ledger-export/ }).click();
	const dialog = page.getByRole('dialog');
	await expect(dialog.getByText('Two copies of ledger-export disagree')).toBeVisible();
	await expect(dialog).toContainText(/split [^,]* ago(?! ago)/);
	await expect(dialog.getByRole('radio', { name: /worker-1/ })).toHaveAttribute(
		'aria-checked',
		'true',
	);
	const confirm = dialog.getByRole('button', { name: 'Keep worker-1, discard the rest' });
	await expect(confirm).toBeDisabled();
	await dialog.getByRole('textbox').fill('ledger-export');
	await confirm.click();
	await expect(dialog).toHaveCount(0);
	await expect(main.getByText('Keeping the copy on worker-1, the rest rebuild')).toBeVisible();
	await expect(main.getByRole('button', { name: /^Keep one copy/ })).toHaveCount(0);
});

test('two split disks of one VM are confirmed one disk at a time', async ({ page }) => {
	await setScenario(page, 'replicatedsplit');
	await login(page);
	await page.goto('/storage?tab=replication');
	const main = page.locator('main');
	await main.getByRole('button', { name: 'Keep one copy of db-1 rootdisk' }).click();
	const dialog = page.getByRole('dialog');
	await expect(dialog.getByText('Two copies of db-1 rootdisk disagree')).toBeVisible();
	await expect(dialog).toContainText('pvc-db1a');
	const confirm = dialog.getByRole('button', { name: 'Keep worker-1, discard the rest' });
	await dialog.getByRole('textbox').fill('db-1');
	await expect(confirm).toBeDisabled();
	await dialog.getByRole('textbox').fill('db-1 data');
	await expect(confirm).toBeDisabled();
	await dialog.getByRole('textbox').fill('db-1 rootdisk');
	await expect(confirm).toBeEnabled();
	await dialog.getByRole('button', { name: 'Cancel' }).click();

	await main.getByRole('button', { name: 'Keep one copy of db-1 data' }).click();
	await expect(dialog.getByText('Two copies of db-1 data disagree')).toBeVisible();
	await expect(dialog).toContainText('pvc-db1b');
});

test('moving copies off a host shows what stays and why first', async ({ page }) => {
	await setScenario(page, 'replicated');
	await login(page);
	await page.goto('/storage?tab=replication&down=worker-1');
	const main = page.locator('main');
	await main.getByRole('button', { name: 'Move copies off worker-1' }).click();
	const dialog = page.getByRole('dialog');
	await expect(dialog.getByText('Its copies disagree: resolve that first.')).toBeVisible();
	await expect(dialog.getByText('A copy is still catching up.')).toBeVisible();
	await expect(dialog.getByRole('heading', { name: 'Leaves worker-1' })).toBeVisible();
	await expect(dialog.getByText('web-1 rootdisk', { exact: true })).toBeVisible();
	const confirm = dialog.getByRole('button', { name: /^Move \d+ cop/ });
	await expect(confirm).toBeDisabled();
	await dialog.getByRole('textbox').fill('worker-1');
	await confirm.click();
	await expect(dialog).toHaveCount(0);
	await expect(page.getByText(/moving off worker-1/)).toBeVisible();
});

test('two claims named alike each keep their row in the move', async ({ page }) => {
	await setScenario(page, 'replicatedalike');
	await login(page);
	await page.goto('/storage?tab=replication&down=worker-1');
	await page.locator('main').getByRole('button', { name: 'Move copies off worker-1' }).click();
	const dialog = page.getByRole('dialog');
	await expect(dialog.getByRole('button', { name: 'Move 2 copies' })).toBeVisible();
	await expect(dialog.getByRole('listitem')).toHaveCount(2);
	await expect(dialog.getByRole('listitem').filter({ hasText: 'data' })).toHaveCount(2);
});

test('a caller who may read but not repair is offered no repair', async ({ page }) => {
	await setScenario(page, 'replicatedreadonly');
	await login(page);
	await page.goto('/storage?tab=replication&down=worker-1');
	const main = page.locator('main');
	await expect(main.getByRole('heading', { name: 'If worker-1 goes down' })).toBeVisible();
	await expect(main.getByRole('button', { name: /^Move copies off/ })).toHaveCount(0);
	await expect(main.getByRole('button', { name: /^Keep one copy/ })).toHaveCount(0);
});

test('a VM page says where its copies are and what a lost host does', async ({ page }) => {
	await setScenario(page, 'replicated');
	await login(page);
	await page.goto('/vm/db-prod/db-1');
	const card = page.locator('main').getByRole('region', { name: 'Replicated disks' });
	await expect(card.getByText('rootdisk', { exact: true })).toBeVisible();
	await expect(card.getByText('Copy on worker-3 catching up, 64%')).toBeVisible();
	await expect(card.getByText('Offline until worker-1 returns')).toBeVisible();
	await expect(card.getByRole('link', { name: 'Where every copy lives' })).toBeVisible();
});

test('a tenant reads a VM disk standing and no host', async ({ page }) => {
	await setScenario(page, 'replicatedtenant');
	await login(page);
	await page.goto('/vm/db-prod/db-1');
	const card = page.locator('main').getByRole('region', { name: 'Replicated disks' });
	await expect(card.getByText('Catching up, 64%')).toBeVisible();
	await expect(card).not.toContainText('worker-');
	await expect(card.getByRole('link', { name: 'Where every copy lives' })).toHaveCount(0);
});

test('a VM with no replicated disk has no such card', async ({ page }) => {
	await setScenario(page, 'replicated');
	await login(page);
	await page.goto('/vm/batch-prod/batch-1');
	await expect(
		page.locator('main').getByRole('region', { name: 'Replicated disks' }),
	).toBeVisible();
	await setScenario(page, 'base');
	await page.goto('/vm/batch-prod/batch-1');
	await expect(page.locator('main').getByRole('heading', { name: 'Placement' })).toBeVisible();
	await expect(page.locator('main').getByRole('region', { name: 'Replicated disks' })).toHaveCount(
		0,
	);
});

test('without r-drbd there is no Replication tab', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/storage');
	await expect(page.locator('main').getByRole('link', { name: /^Classes \d+$/ })).toBeVisible();
	await expect(page.locator('main').getByRole('link', { name: 'Replication' })).toHaveCount(0);
});

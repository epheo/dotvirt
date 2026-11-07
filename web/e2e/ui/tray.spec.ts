import type { Page } from '@playwright/test';
import { expect, login, openVM, setScenario, test } from './fx';

// The change tray past the pull request: applying, live, refused. What it
// follows is persisted, so each test seeds it and reloads, which also holds
// the tray to following across a reload.

const MERGE = 'ab12cd34ef' + '0'.repeat(30);
const merged = (over: Record<string, unknown> = {}) => ({
	project: 'team-web',
	prNumber: 40,
	title: 'web-2: add data disk',
	prURL: 'https://forge.example/dotvirt/team-web/pulls/40',
	mergedAt: Date.now() - 60_000,
	hash: MERGE,
	...over,
});

// Seeded before the app boots: the running page would write its own list over it.
async function following(page: Page, entries: unknown[]) {
	await page.addInitScript(
		(v) => localStorage.setItem('dotvirt.tray.followed', JSON.stringify(v)),
		entries,
	);
	await page.reload();
}

const trayOf = (page: Page) => page.getByRole('complementary', { name: 'Changes in flight' });

test('a merge that runs says what is live, offers undo, and closes by itself', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await following(page, [merged({ lines: ['web-2: Disk data added'] })]);
	const tray = trayOf(page);
	await expect(tray.getByText('Live on the cluster')).toBeVisible();
	// It opened for the merge; the PR still in review keeps to its header.
	await expect(tray.getByText('PR #41 open')).toBeVisible();
	await expect(tray.getByRole('button', { name: 'Copy link for a reviewer' })).toHaveCount(0);
	await expect(tray.getByText('web-2: Disk data added')).toBeVisible();
	await expect(tray.getByText(/Closes in \d+ s/)).toBeVisible();
	await tray.getByRole('button', { name: 'Undo this change' }).click();
	await expect(page.getByRole('dialog').getByText('Undo this change?')).toBeVisible();
});

test('a merge on its way names the steps and who restarts', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await following(page, [merged({ hash: 'ff'.repeat(20), restarts: ['web-2'] })]);
	const tray = trayOf(page);
	// Waiting on Argo is nobody's action: the tray stays a pill until opened.
	await tray.getByRole('button', { name: 'Show' }).click();
	const lane = tray.locator('section', { hasText: 'PR #40 merged' });
	await expect(lane.getByText('Git main updated')).toBeVisible();
	await expect(lane.getByText('Argo CD picked up the merge')).toBeVisible();
	await expect(lane.getByText('Applying; web-2 restarts once')).toBeVisible();
});

test('a refused merge offers the fix and the undo', async ({ page }) => {
	await setScenario(page, 'drift');
	await login(page);
	await following(page, [merged()]);
	const tray = trayOf(page);
	await expect(tray.getByText('The cluster refused PR #40')).toBeVisible();
	await expect(tray.getByRole('link', { name: 'Fix and propose again' })).toBeVisible();
	await tray.getByRole('button', { name: 'Undo the merge' }).click();
	await expect(page.getByRole('dialog').getByText('Undo this change?')).toBeVisible();
});

test('a PR in review hands its link to a reviewer', async ({ page, context }) => {
	await context.grantPermissions(['clipboard-read', 'clipboard-write']);
	await setScenario(page, 'base');
	await login(page);
	const tray = trayOf(page);
	await tray.getByRole('button', { name: 'Show' }).click();
	await tray.getByRole('button', { name: 'Copy link for a reviewer' }).click();
	await expect(page.getByText('Link to PR #41 copied. Send it to a reviewer.')).toBeVisible();
	expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
		'https://forge.example/dotvirt/team-db/pulls/41',
	);
});

test('a folded draft still opens its review in one click', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await openVM(page, 'web-1');
	await page.getByRole('button', { name: 'Change vCPU' }).click();
	await page.getByRole('button', { name: 'One vCPU more' }).click();
	await page.getByRole('button', { name: 'Stage', exact: true }).click();
	const tray = trayOf(page);
	await tray.getByRole('button', { name: 'Fold' }).click();
	await expect(tray.getByText(/1 staged in team-web/)).toBeVisible();
	await tray.getByRole('button', { name: 'Review', exact: true }).click();
	await expect(page.getByRole('dialog').getByText('Propose to team-web')).toBeVisible();
});

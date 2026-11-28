import type { Page } from '@playwright/test';
import { expect, login, setScenario, test } from './fx';

// What a frame re-reads on a page. The stream hands down a fresh VM object
// whenever any of its fields moves, and each read loads again only when the
// string it is keyed on moves: a merge, a field drift compares, a right.

// Counts the page's fetch() calls as they are made. Read back in the page, a
// read the frame triggered is already counted once its render is visible.
async function fetchCounter(page: Page) {
	await page.addInitScript(() => {
		const calls: string[] = [];
		Object.assign(window, { __fetches: calls });
		const fetch = window.fetch.bind(window);
		window.fetch = (input, init) => {
			const url = input instanceof Request ? input.url : String(input);
			calls.push(new URL(url, location.href).pathname);
			return fetch(input, init);
		};
	});
	return (path: string) =>
		page.evaluate(
			(p) => (window as unknown as { __fetches: string[] }).__fetches.filter((u) => u === p).length,
			path,
		);
}

// resource()'s RETRY_MS: the specs never import app code.
const RETRY_MS = 15_000;

const vmPath = (name: string) => `/api/vms/web-prod/${name}`;
const VM = vmPath('web-1');

// One frame, the fixture's object moved as the server would see it.
async function post(page: Page, path: string, fields: Record<string, unknown>) {
	const res = await page.request.post(`/__fixture/${path}`, { data: fields });
	expect(res.ok()).toBeTruthy();
}

for (const tab of ['summary', 'changes'])
	test(`run-state frames re-read nothing on a VM's ${tab} tab`, async ({ page }) => {
		const fetches = await fetchCounter(page);
		await setScenario(page, 'base');
		await login(page);
		await page.goto(`/vm/web-prod/web-1?tab=${tab}`);
		await expect(page.getByText(/^Running on worker-1/).first()).toBeVisible();
		await expect.poll(() => fetches(`${VM}/history`)).toBe(1);
		expect(await fetches(`${VM}/drift`)).toBe(1);

		for (const [i, node] of ['worker-2', 'worker-3', 'worker-1'].entries()) {
			await post(page, 'vm/web-prod/web-1', {
				nodeName: node,
				guestIP: `10.128.1.${i}`,
				ips: [`10.128.1.${i}`],
			});
			await expect(page.getByText(new RegExp(`^Running on ${node}`)).first()).toBeVisible();
		}
		expect(await fetches(`${VM}/drift`)).toBe(1);
		expect(await fetches(`${VM}/history`)).toBe(1);
	});

test("a frame where git moves the VM's declared fields re-reads the drift alone", async ({
	page,
}) => {
	// The frame's declared fields are git's: a merge moves them while the VM is
	// already OutOfSync and before Argo applies it, so the verdict alone would
	// not.
	const fetches = await fetchCounter(page);
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/vm/web-prod/web-1');
	await expect.poll(() => fetches(`${VM}/history`)).toBe(1);
	await post(page, 'vm/web-prod/web-1', { labels: { app: 'web-1', tier: 'edge' } });
	await expect.poll(() => fetches(`${VM}/drift`)).toBe(2);
	expect(await fetches(`${VM}/history`)).toBe(1);
});

test('a drift read that failed is tried again on its own', async ({ page }) => {
	await page.clock.install();
	await setScenario(page, 'base');
	await page.request.post('/__fixture/fail/drift/1');
	await login(page);
	await page.goto('/vm/web-prod/web-1');
	const liveVsGit = page.locator('main').getByText('Live vs git').locator('..');
	await expect(liveVsGit).toContainText('—');
	await page.clock.fastForward(RETRY_MS);
	await expect(liveVsGit).toContainText('Identical');
});

for (const tab of ['summary', 'changes'])
	test(`a merge re-reads a VM's history on its ${tab} tab before Argo applies it`, async ({
		page,
	}) => {
		const fetches = await fetchCounter(page);
		await setScenario(page, 'base');
		await login(page);
		await page.goto(`/vm/web-prod/web-2?tab=${tab}`);
		await expect.poll(() => fetches(`${vmPath('web-2')}/history`)).toBe(1);
		// Argo's applied revision stays where it was; only the base head moves.
		await post(page, 'project/team-web', { head: 'f00d'.repeat(10) });
		await expect.poll(() => fetches(`${vmPath('web-2')}/history`)).toBe(2);
		expect(await fetches(`${vmPath('web-2')}/drift`)).toBe(1);
	});

test('history asks again once the caller no longer reads the repo', async ({ page }) => {
	const fetches = await fetchCounter(page);
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/vm/web-prod/web-2?tab=changes');
	await expect(page.getByText('web-2: add data disk').first()).toBeVisible();
	expect(await fetches(`${vmPath('web-2')}/history`)).toBe(1);
	await post(page, 'caps/web-prod', { project: { repoRead: false } });
	await expect.poll(() => fetches(`${vmPath('web-2')}/history`)).toBe(2);
});

test('a VM the caller may no longer list takes its drift and history with it', async ({ page }) => {
	// Listing the VM is the right both reads need: the frame drops the VM with
	// it, and the page shows neither old answer.
	const fetches = await fetchCounter(page);
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/vm/web-prod/web-2');
	await expect(page.getByText('Last change: web-2: add data disk')).toBeVisible();
	await post(page, 'namespace/web-prod', { vms: [] });
	await expect(page.getByText('web-prod/web-2 is not in the inventory')).toBeVisible();
	await expect(page.getByText('web-2: add data disk')).toHaveCount(0);
	await expect(page.getByText('Live vs git')).toHaveCount(0);
	expect(await fetches(`${vmPath('web-2')}/drift`)).toBe(1);
	expect(await fetches(`${vmPath('web-2')}/history`)).toBe(1);
});

test('drift asks again when the caller may no longer change VMs there', async ({ page }) => {
	// The drift answer carries the resync verdict.
	const fetches = await fetchCounter(page);
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/vm/web-prod/web-1');
	await expect.poll(() => fetches(`${VM}/drift`)).toBe(1);
	await post(page, 'namespace/web-prod', { canAuthor: false, canAuthorVMs: false });
	await expect.poll(() => fetches(`${VM}/drift`)).toBe(2);
});

test("a network's recent changes ask again once the caller no longer reads the repo", async ({
	page,
}) => {
	const fetches = await fetchCounter(page);
	const history = '/api/objects/network/web-prod/web-net/history';
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/networking/web-prod/web-net');
	await expect.poll(() => fetches(history)).toBe(1);
	expect(await fetches('/api/networks/rules')).toBe(1);
	await post(page, 'caps/web-prod', { project: { repoRead: false } });
	await expect.poll(() => fetches(history)).toBe(2);
});

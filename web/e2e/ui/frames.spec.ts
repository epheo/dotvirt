import type { Page } from '@playwright/test';
import { expect, login, setScenario, test } from './fx';

// A frame that moves only a VM's run state re-reads nothing on its page: the
// stream hands down a fresh VM object whenever any of its fields moves, and
// each read loads again only when the string it is keyed on moves.

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

const VM = '/api/vms/web-prod/web-1';

// One frame, as the reflector sees web-1 move.
async function move(page: Page, fields: Record<string, unknown>) {
	const res = await page.request.post('/__fixture/vm/web-prod/web-1', { data: fields });
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
			await move(page, { nodeName: node, guestIP: `10.128.1.${i}`, ips: [`10.128.1.${i}`] });
			await expect(page.getByText(new RegExp(`^Running on ${node}`)).first()).toBeVisible();
		}
		expect(await fetches(`${VM}/drift`)).toBe(1);
		expect(await fetches(`${VM}/history`)).toBe(1);
	});

test('a frame that moves a field drift compares re-reads the drift alone', async ({ page }) => {
	// A second edit outside git leaves the verdict OutOfSync: only the
	// compared fields can say the diff moved.
	const fetches = await fetchCounter(page);
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/vm/web-prod/web-1');
	await expect.poll(() => fetches(`${VM}/history`)).toBe(1);
	await move(page, { labels: { app: 'web-1', tier: 'edge' } });
	await expect.poll(() => fetches(`${VM}/drift`)).toBe(2);
	expect(await fetches(`${VM}/history`)).toBe(1);
});

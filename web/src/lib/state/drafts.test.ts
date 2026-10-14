import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { DraftView } from '$lib/api';

// The drafts summary is refreshed from several places at once (the layout's
// keyed effect, staging callbacks, the dock). Response order is not request
// order, and sign-out can land mid-flight: the store must apply only the newest
// refresh, and nothing from before a reset.

const api = vi.hoisted(() => ({
	draftsByProject: vi.fn(),
	Unauthorized: class extends Error {},
	vmKey: (v: { namespace: string; name: string }) => `${v.namespace}/${v.name}`,
}));
vi.mock('$lib/api', () => api);
const inv = vi.hoisted(() => ({
	inventory: {} as unknown,
	canManage: false,
	projectNames: ['web-prod'],
}));
vi.mock('$lib/state/inventory.svelte', () => ({ inventory: inv }));

import { drafts } from '$lib/state/drafts.svelte';

function deferred<T>() {
	let resolve!: (v: T) => void;
	const promise = new Promise<T>((res) => (resolve = res));
	return { promise, resolve };
}

const view = (count: number) => ({
	drafts: [
		{ project: 'web-prod', draft: { base: 'main', branch: 'draft', count, items: [] } as DraftView },
	],
	failed: [] as { project: string; error: string }[],
});

describe('drafts.refresh', () => {
	beforeEach(() => {
		drafts.reset();
		api.draftsByProject.mockReset();
	});

	it('applies only the newest refresh when an older one resolves last', async () => {
		const first = deferred<ReturnType<typeof view>>();
		const second = deferred<ReturnType<typeof view>>();
		api.draftsByProject.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);

		const r1 = drafts.refresh();
		const r2 = drafts.refresh();
		second.resolve(view(2));
		await r2;
		expect(drafts.count).toBe(2);
		expect(drafts.refreshing).toBe(false);

		first.resolve(view(1));
		await r1;
		expect(drafts.count).toBe(2);
		expect(drafts.refreshing).toBe(false);
	});

	it('drops a response that lands after sign-out', async () => {
		const d = deferred<ReturnType<typeof view>>();
		api.draftsByProject.mockReturnValueOnce(d.promise);

		const r = drafts.refresh();
		drafts.reset();
		d.resolve(view(3));
		await r;
		expect(drafts.count).toBe(0);
		expect(drafts.loaded).toBe(false);
		expect(drafts.refreshing).toBe(false);
	});

	it('keeps per-project failures beside the drafts that loaded', async () => {
		api.draftsByProject.mockResolvedValueOnce({
			...view(1),
			failed: [{ project: 'db', error: 'forge down' }],
		});
		await drafts.refresh();
		expect(drafts.count).toBe(1);
		expect(drafts.failed).toEqual([{ project: 'db', error: 'forge down' }]);
		expect(drafts.loaded).toBe(true);
	});

	it('surfaces a failed refresh instead of a skeleton forever, and clears it on retry', async () => {
		api.draftsByProject.mockRejectedValueOnce(new Error('network down'));
		await drafts.refresh();
		expect(drafts.loaded).toBe(false);
		expect(drafts.error).toBe('network down');
		expect(drafts.refreshing).toBe(false);

		api.draftsByProject.mockResolvedValueOnce(view(0));
		await drafts.refresh();
		expect(drafts.error).toBe('');
		expect(drafts.loaded).toBe(true);
	});
});

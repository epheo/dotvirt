import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { DraftView } from '#lib/api.js';

// The drafts summary is refreshed from several places at once (the layout's
// keyed effect, staging callbacks, the dock). Response order is not request
// order, and sign-out can land mid-flight: the store must apply only the newest
// refresh, and nothing from before a reset.

const mod = vi.hoisted(() => {
	class Unauthorized extends Error {}
	return {
		api: { drafts: vi.fn() },
		Unauthorized,
		vmKey: (v: { namespace: string; name: string }) => `${v.namespace}/${v.name}`,
	};
});
vi.mock('#lib/api.js', () => mod);

import { drafts } from '#lib/state/drafts.svelte.js';

function deferred<T>() {
	let resolve!: (v: T) => void;
	const promise = new Promise<T>((res) => (resolve = res));
	return { promise, resolve };
}

const view = (count: number) => ({
	drafts: [
		{
			project: 'web-prod',
			draft: { base: 'main', branch: 'draft', count, items: [] } as DraftView,
		},
	],
	failed: [] as { project: string; error: string }[],
});

describe('drafts.refresh', () => {
	beforeEach(() => {
		drafts.reset();
		mod.api.drafts.mockReset();
	});

	it('applies only the newest refresh when an older one resolves last', async () => {
		const first = deferred<ReturnType<typeof view>>();
		const second = deferred<ReturnType<typeof view>>();
		mod.api.drafts.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);

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
		mod.api.drafts.mockReturnValueOnce(d.promise);

		const r = drafts.refresh();
		drafts.reset();
		d.resolve(view(3));
		await r;
		expect(drafts.count).toBe(0);
		expect(drafts.loaded).toBe(false);
		expect(drafts.refreshing).toBe(false);
	});

	it('keeps per-project failures beside the drafts that loaded', async () => {
		mod.api.drafts.mockResolvedValueOnce({
			...view(1),
			failed: [{ project: 'db', error: 'forge down' }],
		});
		await drafts.refresh();
		expect(drafts.count).toBe(1);
		expect(drafts.failed).toEqual([{ project: 'db', error: 'forge down' }]);
		expect(drafts.loaded).toBe(true);
	});

	it('a failed call keeps the last summary, says why, and clears on the next answer', async () => {
		mod.api.drafts.mockResolvedValueOnce(view(2));
		await drafts.refresh();
		mod.api.drafts.mockRejectedValueOnce(new Error('503 Service Unavailable'));
		await drafts.refresh();
		expect(drafts.count).toBe(2);
		expect(drafts.error).not.toBe('');
		expect(drafts.refreshing).toBe(false);
		mod.api.drafts.mockResolvedValueOnce(view(1));
		await drafts.refresh();
		expect(drafts.error).toBe('');
		expect(drafts.count).toBe(1);
	});

	it('a 401 sets no error: sign-out handles it', async () => {
		mod.api.drafts.mockRejectedValueOnce(new mod.Unauthorized());
		await drafts.refresh();
		expect(drafts.error).toBe('');
		expect(drafts.loaded).toBe(false);
	});
});

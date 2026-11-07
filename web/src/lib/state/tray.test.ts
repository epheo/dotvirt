import { afterEach, describe, expect, it, vi } from 'vitest';
import type { TaskEntry } from '#lib/api.js';

const api = vi.hoisted(() => ({ api: { history: vi.fn() } }));
vi.mock('#lib/api.js', () => api);

import { tray } from './tray.svelte';

const HASH = 'ab12cd34ef' + '0'.repeat(30);
const mergedFeed: TaskEntry[] = [
	{
		kind: 'merge',
		verb: 'Merged',
		project: 'web',
		prNumber: 40,
		ok: true,
		at: '2026-11-07T10:00:00Z',
	},
];
const commit = {
	hash: HASH,
	shortHash: HASH.slice(0, 8),
	message: '',
	title: '',
	author: 'a',
	when: '',
	prNumber: 40,
};

afterEach(() => {
	tray.reset();
	vi.useRealTimers();
	api.api.history.mockReset();
});

describe('tray history reads', () => {
	it('asks again on its own when the first read misses the merge', async () => {
		vi.useFakeTimers();
		tray.remember({ project: 'web', prNumber: 40, title: 't', prURL: 'u' });
		api.api.history.mockResolvedValueOnce([]).mockResolvedValueOnce([commit]);
		tray.update([], mergedFeed);
		await vi.advanceTimersByTimeAsync(0);
		expect(tray.followed[0].hash).toBeUndefined();
		// No stream event comes: the store's own timer reads again.
		await vi.advanceTimersByTimeAsync(30_000);
		expect(api.api.history).toHaveBeenCalledTimes(2);
		expect(tray.followed[0].hash).toBe(HASH);
	});

	it('asks again after a failed read', async () => {
		vi.useFakeTimers();
		tray.remember({ project: 'web', prNumber: 40, title: 't', prURL: 'u' });
		api.api.history.mockRejectedValueOnce(new Error('forge down')).mockResolvedValueOnce([commit]);
		tray.update([], mergedFeed);
		await vi.advanceTimersByTimeAsync(30_000);
		expect(tray.followed[0].hash).toBe(HASH);
		expect(tray.commits('web')).toEqual([HASH]);
	});

	it('stops asking once every followed merge is found', async () => {
		vi.useFakeTimers();
		tray.remember({ project: 'web', prNumber: 40, title: 't', prURL: 'u' });
		api.api.history.mockResolvedValue([commit]);
		tray.update([], mergedFeed);
		await vi.advanceTimersByTimeAsync(120_000);
		expect(api.api.history).toHaveBeenCalledTimes(1);
	});
});

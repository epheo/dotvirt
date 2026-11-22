import { afterEach, expect, it, vi } from 'vitest';
import { duration } from '#lib/format.js';
import { flushSync, track } from '../../test/runes.svelte';

afterEach(() => {
	vi.useRealTimers();
});

it('advances an elapsed time on its own, with a timer only while it is read', async () => {
	vi.useFakeTimers({ now: Date.parse('2026-01-01T00:00:00Z') });
	const seen: string[] = [];
	const stop = track(() => duration('2026-01-01T00:00:00Z'), seen);
	for (let i = 0; i < 2; i++) {
		vi.advanceTimersByTime(1000);
		flushSync();
	}
	expect(seen).toEqual(['0s', '1s', '2s']);

	stop();
	await Promise.resolve();
	expect(vi.getTimerCount()).toBe(0);
});

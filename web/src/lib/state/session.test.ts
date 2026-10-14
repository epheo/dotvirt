import { describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => {
	class Unauthorized extends Error {}
	return { api: { me: vi.fn() }, Unauthorized, retryDelay: () => 1000 };
});
vi.mock('$lib/api', () => api);

import { session } from './session.svelte';

describe('session.check', () => {
	it('retries a non-401 failure, showing unreachable, until the server answers', async () => {
		vi.useFakeTimers();
		try {
			api.api.me
				.mockRejectedValueOnce(new Error('Failed to fetch'))
				.mockResolvedValueOnce({ username: 'ada', groups: [] });
			const done = session.check();
			await vi.advanceTimersByTimeAsync(0);
			expect(session.checking).toBe(true);
			expect(session.unreachable).toBe('Failed to fetch');
			await vi.advanceTimersByTimeAsync(1000);
			await done;
			expect(session.user?.username).toBe('ada');
			expect(session.unreachable).toBe('');
			expect(session.checking).toBe(false);
		} finally {
			vi.useRealTimers();
		}
	});

	it('retryNow skips the backoff wait', async () => {
		api.api.me
			.mockRejectedValueOnce(new Error('503'))
			.mockResolvedValueOnce({ username: 'ada', groups: [] });
		const done = session.check();
		await vi.waitFor(() => expect(session.unreachable).toBe('503'));
		session.retryNow();
		await done;
		expect(session.user?.username).toBe('ada');
	});

	it('goes to Login only on a 401', async () => {
		api.api.me.mockRejectedValueOnce(new api.Unauthorized());
		await session.check();
		expect(session.user).toBeNull();
		expect(session.checking).toBe(false);
		expect(session.unreachable).toBe('');
	});
});

import { describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => {
	class Unauthorized extends Error {}
	class HttpError extends Error {
		constructor(
			m: string,
			readonly status: number,
		) {
			super(m);
		}
	}
	return { api: { me: vi.fn() }, Unauthorized, HttpError, retryDelay: () => 1000 };
});
vi.mock('#lib/api.js', () => api);

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
			expect(session.bootError).toEqual({ answered: false, detail: 'Failed to fetch' });
			await vi.advanceTimersByTimeAsync(1000);
			await done;
			expect(session.user?.username).toBe('ada');
			expect(session.bootError).toBeNull();
			expect(session.checking).toBe(false);
		} finally {
			vi.useRealTimers();
		}
	});

	it('retryNow skips the backoff wait', async () => {
		api.api.me
			.mockRejectedValueOnce(new api.HttpError('/api/me: 500 boom', 500))
			.mockResolvedValueOnce({ username: 'ada', groups: [] });
		const done = session.check();
		await vi.waitFor(() =>
			expect(session.bootError).toEqual({ answered: true, detail: '/api/me: 500 boom' }),
		);
		session.retryNow();
		await done;
		expect(session.user?.username).toBe('ada');
	});

	it('goes to Login only on a 401', async () => {
		api.api.me.mockRejectedValueOnce(new api.Unauthorized());
		await session.check();
		expect(session.user).toBeNull();
		expect(session.checking).toBe(false);
		expect(session.bootError).toBeNull();
	});
});

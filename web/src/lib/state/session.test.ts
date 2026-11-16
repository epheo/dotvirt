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
	return { api: { me: vi.fn(), logout: vi.fn() }, Unauthorized, HttpError, retryDelay: () => 1000 };
});
vi.mock('#lib/api.js', () => api);

import { navigations } from '../../test/app-navigation';
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

describe('session.logout', () => {
	const signedIn = () => (session.user = { username: 'ada', groups: [] });

	it('keeps the session when sign-out fails, says so, and a Retry finishes it', async () => {
		signedIn();
		let answer!: () => void;
		api.api.logout
			.mockRejectedValueOnce(new api.HttpError('forge down', 503))
			.mockReturnValueOnce(new Promise<void>((r) => (answer = r)));
		await session.logout();
		expect(session.user?.username).toBe('ada');
		expect(session.logoutError).toBe('Sign-out did not complete. Your session is still active.');
		expect(session.signingOut).toBe(false);

		const retry = session.logout();
		expect(session.signingOut).toBe(true);
		answer();
		await retry;
		expect(session.user).toBeNull();
		expect(session.signedOutHere).toBe(true);
		expect(navigations.at(-1)).toBe('/?signed_out');
		session.signIn({ username: 'ada', groups: [] });
		expect(session.signedOutHere).toBe(false);
		expect(session.logoutError).toBe('');
	});

	it('keeps the session when the request never reached the server', async () => {
		signedIn();
		navigations.length = 0;
		api.api.logout.mockRejectedValueOnce(new TypeError('Failed to fetch'));
		await session.logout();
		expect(navigations).toEqual([]);
		expect(session.user?.username).toBe('ada');
		expect(session.logoutError).not.toBe('');
	});

	it('reads a 401 as already signed out', async () => {
		signedIn();
		navigations.length = 0;
		api.api.logout.mockRejectedValueOnce(new api.Unauthorized());
		await session.logout();
		expect(session.user).toBeNull();
		expect(session.signedOutHere).toBe(true);
		expect(navigations).toEqual(['/?signed_out']);
		expect(session.logoutError).toBe('');
	});
});

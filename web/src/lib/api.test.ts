import { describe, expect, it, vi } from 'vitest';
import { draftsByProject, retryDelay, Unauthorized, withRetry } from '$lib/api';

describe('retryDelay', () => {
	it('starts at 0.5s and doubles per attempt', () => {
		expect([1, 2, 3, 4, 5].map(retryDelay)).toEqual([500, 1000, 2000, 4000, 8000]);
	});

	it('caps at 16s', () => {
		expect(retryDelay(6)).toBe(16000);
		expect(retryDelay(7)).toBe(16000);
		expect(retryDelay(100)).toBe(16000);
	});

	it('clamps below the first attempt to 0.5s', () => {
		expect(retryDelay(0)).toBe(500);
		expect(retryDelay(-3)).toBe(500);
	});
});

describe('withRetry', () => {
	it('backs off 1s doubling and resolves once fn succeeds', async () => {
		vi.useFakeTimers();
		try {
			let calls = 0;
			const fn = vi.fn(() =>
				++calls < 3 ? Promise.reject(new Error('boom')) : Promise.resolve('ok'),
			);
			const p = withRetry(fn);
			await vi.advanceTimersByTimeAsync(1000);
			expect(fn).toHaveBeenCalledTimes(2);
			await vi.advanceTimersByTimeAsync(2000);
			expect(fn).toHaveBeenCalledTimes(3);
			await expect(p).resolves.toBe('ok');
		} finally {
			vi.useRealTimers();
		}
	});

	it('rejects with the last error after the retries run out', async () => {
		vi.useFakeTimers();
		try {
			const fn = vi.fn(() => Promise.reject(new Error('down')));
			const p = withRetry(fn, 2);
			p.catch(() => {}); // the assertion below attaches after the timers run
			await vi.advanceTimersByTimeAsync(3000);
			expect(fn).toHaveBeenCalledTimes(3);
			await expect(p).rejects.toThrow('down');
		} finally {
			vi.useRealTimers();
		}
	});

	it('never retries a 401', async () => {
		const fn = vi.fn(() => Promise.reject(new Unauthorized()));
		await expect(withRetry(fn)).rejects.toBeInstanceOf(Unauthorized);
		expect(fn).toHaveBeenCalledTimes(1);
	});
});

describe('draftsByProject', () => {
	it('reports a failed project, skips a 403, and keeps non-empty drafts', async () => {
		const answers: Record<string, Response> = {
			web: Response.json({ base: 'main', branch: 'd', count: 2, items: [] }),
			empty: Response.json({ base: 'main', branch: 'd', count: 0, items: [] }),
			platform: new Response('not authorized', { status: 403 }),
			db: new Response('forge unavailable', { status: 503 }),
		};
		vi.stubGlobal(
			'fetch',
			vi.fn((url: string) =>
				Promise.resolve(answers[new URL(url, 'http://x').searchParams.get('project')!]),
			),
		);
		try {
			const out = await draftsByProject(['web', 'empty', 'platform', 'db']);
			expect(out.drafts.map((d) => d.project)).toEqual(['web']);
			expect(out.failed).toEqual([
				{ project: 'db', error: '/api/draft?project=db: 503 forge unavailable' },
			]);
		} finally {
			vi.unstubAllGlobals();
		}
	});
});

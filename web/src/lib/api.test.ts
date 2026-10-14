import { describe, expect, it, vi } from 'vitest';
import {
	draftsByProject,
	retryDelay,
	streamInventory,
	Unauthorized,
	withRetry,
	type StreamStatus,
} from '#lib/api.js';

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

describe('streamInventory status', () => {
	class FakeSocket {
		static all: FakeSocket[] = [];
		onopen?: () => void;
		onclose?: () => void;
		onmessage?: (e: { data: string }) => void;
		onerror?: () => void;
		constructor() {
			FakeSocket.all.push(this);
		}
		close() {
			this.onclose?.();
		}
	}

	it('reports live, then reconnecting, then down once backoff hits the cap', async () => {
		vi.useFakeTimers();
		vi.stubGlobal('WebSocket', FakeSocket);
		vi.stubGlobal('location', { protocol: 'http:', host: 'x' });
		vi.stubGlobal(
			'fetch',
			vi.fn(() => Promise.resolve(Response.json({ username: 'ada', groups: [] }))),
		);
		FakeSocket.all = [];
		const seen: StreamStatus[] = [];
		const s = streamInventory(
			() => {},
			undefined,
			(st) => seen.push(st),
		);
		try {
			FakeSocket.all[0].onopen!();
			FakeSocket.all[0].close();
			expect(seen).toEqual(['connecting', 'live', 'reconnecting']);

			// Each retry fails before opening: the session probe passes, backoff grows.
			for (let i = 1; i < 6; i++) {
				await vi.advanceTimersByTimeAsync(retryDelay(i));
				FakeSocket.all[i].close();
				await vi.advanceTimersByTimeAsync(0);
			}
			expect(seen.at(-1)).toBe('down');

			// retryNow skips the 16s wait.
			const before = FakeSocket.all.length;
			s.retryNow();
			expect(FakeSocket.all.length).toBe(before + 1);
			FakeSocket.all.at(-1)!.onopen!();
			expect(seen.at(-1)).toBe('live');
		} finally {
			s.close();
			vi.unstubAllGlobals();
			vi.useRealTimers();
		}
	});

	it('retryNow during the session probe skips the next backoff wait', async () => {
		vi.useFakeTimers();
		vi.stubGlobal('WebSocket', FakeSocket);
		vi.stubGlobal('location', { protocol: 'http:', host: 'x' });
		let answer!: (r: Response) => void;
		vi.stubGlobal(
			'fetch',
			vi.fn(() => new Promise<Response>((r) => (answer = r))),
		);
		FakeSocket.all = [];
		const s = streamInventory(() => {});
		try {
			FakeSocket.all[0].close(); // never opened: probes the session
			s.retryNow();
			answer(Response.json({ username: 'ada', groups: [] }));
			await vi.advanceTimersByTimeAsync(0);
			expect(FakeSocket.all.length).toBe(2);
		} finally {
			s.close();
			vi.unstubAllGlobals();
			vi.useRealTimers();
		}
	});
});

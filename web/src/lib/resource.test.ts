import { describe, expect, it } from 'vitest';
import { Unauthorized } from './api';
import { action, resource, type Resource } from './resource.svelte';
import { box, effectRoot, flushSync } from '../test/runes.svelte';

// resource()/action() carry the async idioms 15+ components lean on: the
// stale-response guard on fast key switches, first-load-only loading, the
// central-401 swallow. Subtle enough to deserve direct tests.

// deferred is a hand-resolved promise, so tests control response order.
function deferred<T>() {
	let resolve!: (v: T) => void;
	let reject!: (e: unknown) => void;
	const promise = new Promise<T>((res, rej) => {
		resolve = res;
		reject = rej;
	});
	return { promise, resolve, reject };
}

const tick = () => new Promise((r) => setTimeout(r, 0));

describe('resource', () => {
	it('loads the initial key and clears loading on data', async () => {
		const d = deferred<string>();
		let r!: Resource<string>;
		const stop = effectRoot(() => {
			r = resource(
				() => 'vm-a',
				() => d.promise,
			);
		});
		expect(r.loading).toBe(true);
		expect(r.data).toBeNull();
		d.resolve('data-a');
		await tick();
		expect(r.data).toBe('data-a');
		expect(r.loading).toBe(false);
		expect(r.failed).toBe(false);
		stop();
	});

	it('drops a stale response when the key moves mid-flight', async () => {
		// The classic fast-click bug: open vm-a, click vm-b before a's fetch
		// lands - a's late response must never paint over b's view.
		const key = box('vm-a');
		const inflight = new Map<string, ReturnType<typeof deferred<string>>>();
		let r!: Resource<string>;
		const stop = effectRoot(() => {
			r = resource(
				() => key.value,
				(k) => {
					const d = deferred<string>();
					inflight.set(k, d);
					return d.promise;
				},
				{ reset: true },
			);
		});
		key.value = 'vm-b';
		flushSync();
		inflight.get('vm-b')!.resolve('data-b');
		await tick();
		inflight.get('vm-a')!.resolve('data-a'); // the stale one lands late
		await tick();
		expect(r.data).toBe('data-b');
		stop();
	});

	it('reset:true blanks on a key change, reset:false keeps the old view', async () => {
		for (const reset of [true, false]) {
			const key = box('a');
			const inflight = new Map<string, ReturnType<typeof deferred<string>>>();
			let r!: Resource<string>;
			const stop = effectRoot(() => {
				r = resource(
					() => key.value,
					(k) => {
						const d = deferred<string>();
						inflight.set(k, d);
						return d.promise;
					},
					{ reset },
				);
			});
			inflight.get('a')!.resolve('data-a');
			await tick();
			key.value = 'b';
			flushSync();
			expect(r.data).toBe(reset ? null : 'data-a');
			stop();
		}
	});

	it('refresh never re-blanks: loading is first-load-only', async () => {
		let n = 0;
		let r!: Resource<number>;
		const stop = effectRoot(() => {
			r = resource(
				() => 'k',
				async () => ++n,
			);
		});
		await tick();
		expect(r.data).toBe(1);
		const p = r.refresh();
		expect(r.loading).toBe(false); // a poll refresh must not flash the skeleton
		expect(r.data).toBe(1);
		await p;
		expect(r.data).toBe(2);
		stop();
	});

	it('a failure sets error and a later success clears it', async () => {
		let fail = true;
		let r!: Resource<string>;
		const stop = effectRoot(() => {
			r = resource(
				() => 'k',
				async () => {
					if (fail) throw new Error('boom');
					return 'ok';
				},
			);
		});
		await tick();
		expect(r.failed).toBe(true);
		expect(r.error).not.toBe('');
		fail = false;
		await r.refresh();
		expect(r.failed).toBe(false);
		expect(r.data).toBe('ok');
		stop();
	});

	it('swallows Unauthorized: the central sink signs out, no local error', async () => {
		let r!: Resource<string>;
		const stop = effectRoot(() => {
			r = resource(
				() => 'k',
				async () => {
					throw new Unauthorized();
				},
			);
		});
		await tick();
		expect(r.failed).toBe(false);
		expect(r.error).toBe('');
		stop();
	});
});

describe('action', () => {
	it('resolves true on success and exposes busy while running', async () => {
		let op!: ReturnType<typeof action>;
		const stop = effectRoot(() => {
			op = action();
		});
		const d = deferred<void>();
		const done = op.run(() => d.promise);
		expect(op.busy).toBe(true);
		d.resolve();
		expect(await done).toBe(true);
		expect(op.busy).toBe(false);
		expect(op.error).toBe('');
		stop();
	});

	it('resolves false with the friendly error on failure, clear() resets', async () => {
		let op!: ReturnType<typeof action>;
		const stop = effectRoot(() => {
			op = action();
		});
		expect(await op.run(() => Promise.reject(new Error('stage failed: denied')))).toBe(false);
		expect(op.error).toContain('denied');
		op.clear();
		expect(op.error).toBe('');
		stop();
	});

	it('swallows Unauthorized without painting an error over the login redirect', async () => {
		let op!: ReturnType<typeof action>;
		const stop = effectRoot(() => {
			op = action();
		});
		expect(await op.run(() => Promise.reject(new Unauthorized()))).toBe(false);
		expect(op.error).toBe('');
		stop();
	});
});

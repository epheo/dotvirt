import { untrack } from 'svelte';
import { Unauthorized } from './api';
import { friendlyError } from './format';
import { pollWhileVisible } from './poll';

// resource owns the keyed-fetch idiom shared by the detail and metrics panels:
// a load keyed on a STABLE identity string (never a per-frame object — the live
// stream hands down fresh objects every frame), a stale-response guard for fast
// key switches, the central-401 swallow, and an optional poll paused while the
// tab is backgrounded. Call during component init (it registers $effects).
//
// loading is true only before the first data for the current key, so a poll
// refresh never blanks the view. reset controls what a key change shows while
// the new load is in flight: true blanks (detail panels — the old object is
// another identity's), false keeps the old data (scope dashboards avoid a
// flash).
export interface Resource<T> {
	readonly data: T | null;
	readonly loading: boolean;
	readonly failed: boolean;
	readonly error: string; // last failure's message; '' when the last load succeeded
	refresh(): Promise<void>;
}

export function resource<T>(
	key: () => string,
	load: (key: string) => Promise<T>,
	// poll: a cadence in ms, or a reactive gate returning one (0 = paused).
	opts: { poll?: number | (() => number); reset?: boolean } = {},
): Resource<T> {
	let data = $state<T | null>(null);
	let loading = $state(false);
	let error = $state('');

	async function run() {
		const k = untrack(key);
		if (data === null) loading = true;
		try {
			const v = await load(k);
			if (key() !== k) return; // the key moved while in flight: stale response
			data = v;
			error = '';
		} catch (e) {
			if (e instanceof Unauthorized) return; // signs out centrally via the api layer
			if (key() !== k) return;
			error = friendlyError(e);
		} finally {
			if (key() === k) loading = false;
		}
	}

	$effect(() => {
		key();
		if (opts.reset) {
			data = null;
			error = '';
		}
		untrack(run);
	});
	const poll = opts.poll;
	if (poll) {
		$effect(() => {
			const ms = typeof poll === 'function' ? poll() : poll;
			if (!ms) return;
			return pollWhileVisible(() => void untrack(run), ms);
		});
	}

	return {
		get data() {
			return data;
		},
		get loading() {
			return loading;
		},
		get failed() {
			return error !== '';
		},
		get error() {
			return error;
		},
		refresh: run,
	};
}

// action owns resource()'s write-side idiom: one busy flag, one error slot, and
// the central-401 swallow — so no dialog hand-rolls the sequence or forgets the
// Unauthorized branch and paints an error banner over the login redirect.
// run resolves true on success, so call sites close on `if (await op.run(...))`.
export interface Action {
	readonly busy: boolean;
	readonly error: string;
	clear(): void;
	run(fn: () => Promise<unknown>): Promise<boolean>;
}

export function action(): Action {
	let busy = $state(false);
	let error = $state('');
	return {
		get busy() {
			return busy;
		},
		get error() {
			return error;
		},
		clear() {
			error = '';
		},
		async run(fn) {
			busy = true;
			error = '';
			try {
				await fn();
				return true;
			} catch (e) {
				if (e instanceof Unauthorized) return false;
				error = friendlyError(e);
				return false;
			} finally {
				busy = false;
			}
		},
	};
}

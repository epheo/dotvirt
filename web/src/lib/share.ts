// Structural sharing for JSON frames: every subtree of `next` deep-equal to its
// counterpart in `prev` is replaced by prev's object, and `prev` itself comes
// back when nothing changed. Keyed {#each} items and $derived compare by
// identity, so unchanged VMs skip all work on a live frame.
//
// `next` must be freshly parsed and unshared: changed subtrees are rewritten in
// place to point at the reused children.
export function share<T>(prev: T, next: T): T {
	return reuse(prev, next) as T;
}

function reuse(prev: unknown, next: unknown): unknown {
	if (prev === next) return prev;
	if (!isObject(prev) || !isObject(next)) return next;
	if (Array.isArray(prev) !== Array.isArray(next)) return next;
	return Array.isArray(next)
		? reuseArray(prev as unknown[], next)
		: reuseRecord(prev as Record<string, unknown>, next as Record<string, unknown>);
}

function reuseRecord(prev: Record<string, unknown>, next: Record<string, unknown>): unknown {
	let same = Object.keys(prev).length === Object.keys(next).length;
	for (const k of Object.keys(next)) {
		if (!Object.hasOwn(prev, k)) {
			same = false;
			continue;
		}
		const v = reuse(prev[k], next[k]);
		next[k] = v;
		if (v !== prev[k]) same = false;
	}
	return same ? prev : next;
}

// Elements pair by identity, not position, so an insert or reorder still reuses
// every unchanged VM. Pairing only picks candidates: reuse() keeps a prev
// element only when it is deep-equal, so a wrong pairing costs sharing, never
// correctness.
function reuseArray(prev: unknown[], next: unknown[]): unknown {
	const byId = identities(prev);
	let same = prev.length === next.length;
	for (let i = 0; i < next.length; i++) {
		const id = byId ? identity(next[i]) : undefined;
		const candidate = id === undefined ? prev[i] : byId!.get(id);
		const v = reuse(candidate, next[i]);
		next[i] = v;
		if (v !== prev[i]) same = false;
	}
	return same ? prev : next;
}

// Null when any element lacks an identity or two share one: fall back to index.
function identities(arr: unknown[]): Map<string, unknown> | null {
	const m = new Map<string, unknown>();
	for (const x of arr) {
		const id = identity(x);
		if (id === undefined || m.has(id)) return null;
		m.set(id, x);
	}
	return m;
}

function identity(x: unknown): string | undefined {
	if (!isObject(x) || Array.isArray(x)) return undefined;
	const o = x as Record<string, unknown>;
	if (typeof o.name !== 'string' && typeof o.namespace !== 'string') return undefined;
	return `${o.kind ?? ''}\0${o.namespace ?? ''}\0${o.name ?? ''}`;
}

function isObject(x: unknown): x is object {
	return typeof x === 'object' && x !== null;
}

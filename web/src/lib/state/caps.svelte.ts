import { untrack } from 'svelte';
import {
	api,
	Unauthorized,
	type ClusterCaps,
	type DesktopCaps,
	type NamespaceCaps,
} from '#lib/api.js';
import { ACCESS_UNKNOWN } from '#lib/authoring.js';

// What the caller may do, from GET /api/capabilities: each field asks what its
// server gate asks, so a control offered here is one the server accepts.
// Missing data reads as false. The cluster answer is read once per session and
// again whenever the networks or tasks fingerprint moves; a namespace's, and
// the desktop acts of a pool there, are read the first time a control asks for
// them, kept, and read again on the same signals. A re-read keeps serving the
// answer it replaces: dropping it would flip every control to checking, and
// remount a console, on each merge.

// A gate is whether a control is offered and, when it is not, the one line
// that says why: still checking, the check failed, or the server's no.
export type Gate = { ok: boolean; why: string };

export const CHECKING = 'Checking your access...';
const OPEN: Gate = { ok: true, why: '' };

// gen is the invalidation the answer was read under.
type Read<T> =
	{ state: 'ok'; value: T; gen: number } | { state: 'failed'; at: number; gen: number };

// A failed read is asked again after this long.
const RETRY_MS = 15_000;

// The desktop acts are keyed apart from the namespaces: no namespace or pool
// name holds a ':' or a '/'.
const DESK = 'desktops:';
const deskKey = (namespace: string, pool: string) => `${DESK}${namespace}/${pool}`;

class CapsStore {
	#cluster = $state<Read<ClusterCaps> | null>(null);
	#ns = $state<Record<string, Read<NamespaceCaps>>>({});
	#desk = $state<Record<string, Read<DesktopCaps>>>({});
	// Reads in flight, outside state: a gate asked during render may start one.
	#asking = new Map<string, Promise<void>>();
	// State, so a gate read before an invalidate or a retry is asked again after.
	#gen = $state(0);
	#retry = $state(0);

	// The cluster answer, or null until it has landed (or when it failed).
	get cluster(): ClusterCaps | null {
		this.#want('');
		return this.#cluster?.state === 'ok' ? this.#cluster.value : null;
	}

	// One namespace's answer, read on first ask.
	ns(namespace: string): NamespaceCaps | null {
		if (!namespace) return null;
		this.#want(namespace);
		const r = this.#ns[namespace];
		return r?.state === 'ok' ? r.value : null;
	}

	// clusterGate offers what pick says the cluster answer allows.
	clusterGate(pick: (c: ClusterCaps) => boolean, denied: string): Gate {
		this.#want('');
		return gate(this.#cluster, pick, denied);
	}

	// nsGate offers what pick says namespace's answer allows.
	nsGate(namespace: string, pick: (n: NamespaceCaps) => boolean, denied: string): Gate {
		if (!namespace) return { ok: false, why: denied };
		this.#want(namespace);
		return gate(this.#ns[namespace], pick, denied);
	}

	// desktopGate offers what pick says the desktop acts in namespace allow, on
	// a desktop of pool ('' for one made one by one).
	desktopGate(
		namespace: string,
		pool: string,
		pick: (d: DesktopCaps) => boolean,
		denied: string,
	): Gate {
		if (!namespace) return { ok: false, why: denied };
		const key = deskKey(namespace, pool);
		this.#want(key);
		return gate(this.#desk[key], pick, denied);
	}

	// Mark every answer stale: the next ask reads again. Called when what the
	// caller may do can have moved (a merge, a network or policy change).
	// Untracked: an effect that invalidates must not come to depend on #gen.
	invalidate() {
		this.#gen = untrack(() => this.#gen) + 1;
		this.#asking.clear();
	}

	// Forget every answer (sign-out): the next caller starts from checking.
	reset() {
		this.invalidate();
		this.#cluster = null;
		this.#ns = {};
		this.#desk = {};
	}

	// A read starts from a gate asked inside a derived or a template, where
	// writing state is not allowed: the write waits a microtask.
	#want(key: string) {
		const gen = this.#gen;
		void this.#retry;
		const have = !key ? this.#cluster : key.startsWith(DESK) ? this.#desk[key] : this.#ns[key];
		const stale =
			!have || have.gen !== gen || (have.state === 'failed' && Date.now() - have.at >= RETRY_MS);
		if (!stale || this.#asking.has(key)) return;
		this.#asking.set(
			key,
			new Promise<void>((done) => queueMicrotask(() => void this.#read(key, gen).finally(done))),
		);
	}

	// nsRead is one namespace's answer once it has landed, for code that
	// waits on it rather than rendering from it (null when the read failed).
	// A read an invalidate overtook is not the answer: it asks again.
	async nsRead(namespace: string): Promise<NamespaceCaps | null> {
		for (;;) {
			const gen = this.#gen;
			this.#want(namespace);
			await this.#asking.get(namespace);
			if (gen === this.#gen) return this.ns(namespace);
		}
	}

	async #read(key: string, gen: number) {
		const desk = key.startsWith(DESK);
		let r: Read<ClusterCaps | NamespaceCaps | DesktopCaps> = {
			state: 'failed',
			at: Date.now(),
			gen,
		};
		try {
			let value;
			if (desk) {
				const [namespace, pool] = key.slice(DESK.length).split('/');
				value = (await api.desktopCapabilities(namespace, pool)).desktops;
			} else {
				const caps = await api.capabilities(key || undefined);
				value = key ? caps.namespace : caps.cluster;
			}
			if (value) r = { state: 'ok', value, gen };
		} catch (e) {
			if (e instanceof Unauthorized) return; // signs out centrally
		}
		if (gen !== this.#gen) return;
		this.#asking.delete(key);
		if (r.state === 'failed') setTimeout(() => this.#retry++, RETRY_MS);
		if (desk) this.#desk = { ...this.#desk, [key]: r as Read<DesktopCaps> };
		else if (key) this.#ns = { ...this.#ns, [key]: r as Read<NamespaceCaps> };
		else this.#cluster = r as Read<ClusterCaps>;
	}
}

function gate<T>(r: Read<T> | null | undefined, pick: (v: T) => boolean, denied: string): Gate {
	if (!r) return { ok: false, why: CHECKING };
	if (r.state === 'failed') return { ok: false, why: ACCESS_UNKNOWN };
	// An answer missing the field (or its object) is a no.
	let ok: boolean;
	try {
		ok = pick(r.value) === true;
	} catch {
		ok = false;
	}
	return ok ? OPEN : { ok: false, why: denied };
}

export const caps = new CapsStore();

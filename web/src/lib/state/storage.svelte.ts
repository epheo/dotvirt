import { untrack } from 'svelte';
import {
	api,
	Unauthorized,
	type Claim,
	type Node,
	type Snapshot,
	type StorageClassInfo,
} from '#lib/api.js';
import { friendlyError } from '#lib/format.js';
import { diskRows, imageUsage, uploadAbility, type UploadAbility } from '#lib/storage.js';
import { caps } from './caps.svelte';
import { templates } from './templates.svelte';
import { inventory } from './inventory.svelte';
import { vmsOff } from './modules.svelte';

// The Storage section's reads beside the live inventory: the class sheet (SA,
// cached server-side), and the caller's snapshots, claims and hosts (live
// reads under the caller's token). Pulled when the section opens and after the
// section's own actions, never polled: a few thousand snapshots per pull is a
// user-opened view, not a broadcast. The tree, the header and every tab read
// the same rows from here.
class StorageStore {
	classes = $state.raw<StorageClassInfo[] | null>(null);
	snapshots = $state.raw<Snapshot[] | null>(null);
	claims = $state.raw<Claim[] | null>(null);
	nodes = $state.raw<Node[] | null>(null);
	// The last pull's failure per source; '' once one succeeds.
	classesError = $state('');
	snapshotsError = $state('');
	// Where the caller may upload, for the Library's drop zone: asked of the
	// namespaces they author in, again only when that set changes.
	upload = $state<UploadAbility | null>(null);
	#uploadKey = '';
	#gen = 0;
	#loaded = false;
	#skippedVMs = false;

	readonly rows = $derived(
		diskRows({
			projects: inventory.projects,
			classes: this.classes ?? [],
			defaultClass: inventory.defaultStorageClass,
			snapshots: this.snapshots,
			claims: this.claims,
			nodes: this.nodes,
		}),
	);
	readonly images = $derived(
		imageUsage(this.rows, inventory.options?.osImages ?? [], templates.list ?? []),
	);
	// Every class name: the cluster's, and any a disk names that it lacks.
	readonly classNames = $derived(
		[
			...new Set([
				...(this.classes ?? []).map((c) => c.name),
				...this.rows.filter((r) => !r.ephemeral && r.storageClass).map((r) => r.storageClass),
			]),
		].sort(),
	);
	readonly hostCount = $derived(
		this.nodes?.length ?? new Set(this.rows.map((r) => r.host).filter(Boolean)).size,
	);

	// First open of the section pulls everything; later calls are no-ops, so
	// the tree and the workspace may both ask.
	ensure() {
		if (this.#loaded) return;
		this.#loaded = true;
		this.refresh();
	}

	async refresh() {
		const gen = this.#gen;
		// Options, templates, snapshots and claims all read VM disks.
		const vms = !vmsOff();
		this.#skippedVMs = !vms;
		if (vms) {
			inventory.loadOptions();
			templates.load();
		}
		await Promise.all([
			this.#pull(
				api.storageClasses,
				(v) => (this.classes = v),
				(e) => (this.classesError = e),
				gen,
			),
			vms && this.refreshSnapshots(),
			vms &&
				this.#pull(
					api.storageClaims,
					(v) => (this.claims = v),
					() => {},
					gen,
				),
			// Node rights are cluster-scoped; without them hosts stay unnamed.
			this.#pull(
				api.nodes,
				(v) => (this.nodes = v),
				() => {},
				gen,
			),
		]);
	}

	// KubeVirt installed while Storage stays open: the reads refresh skipped.
	catchUp() {
		if (this.#skippedVMs && !vmsOff()) untrack(() => this.refresh());
	}

	async ensureUpload(namespaces: string[]) {
		const key = namespaces.join('\0');
		if (key === this.#uploadKey) return;
		this.#uploadKey = key;
		this.upload = null;
		const gen = this.#gen;
		const found = await uploadAbility(
			namespaces,
			async (ns) => (await caps.nsRead(ns)) ?? undefined,
			4,
			caps.cluster?.imageUpload === false,
		);
		if (gen === this.#gen && key === this.#uploadKey) this.upload = found;
	}

	refreshSnapshots(): Promise<void> {
		return this.#pull(
			api.storageSnapshots,
			(v) => (this.snapshots = v),
			(e) => (this.snapshotsError = e),
			this.#gen,
		);
	}

	async #pull<T>(
		load: () => Promise<T>,
		set: (v: T) => void,
		fail: (e: string) => void,
		gen: number,
	) {
		try {
			const v = await load();
			if (gen !== this.#gen) return;
			set(v);
			fail('');
		} catch (e) {
			if (gen !== this.#gen || e instanceof Unauthorized) return; // 401 signs out centrally
			fail(friendlyError(e));
		}
	}

	reset() {
		this.#gen++;
		this.#loaded = false;
		this.#skippedVMs = false;
		this.classes = null;
		this.snapshots = null;
		this.claims = null;
		this.nodes = null;
		this.classesError = '';
		this.snapshotsError = '';
		this.upload = null;
		this.#uploadKey = '';
	}
}

export const storage = new StorageStore();

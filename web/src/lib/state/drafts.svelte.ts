import { draftsByProject, type DraftItem, type DraftView } from '$lib/api';
import { inventory } from './inventory.svelte';

// The synthetic platform-tier project (matches the backend's platformProjectName);
// holds the cluster-scoped network + namespace changeset, proposable by authors.
export const PLATFORM_PROJECT = 'platform';

// Per-project drafts (the Changes panel + header badge). Refreshed when the set
// of projects or PR lanes changes (the layout's keyed effect), when the Changes
// drawer opens, and directly after every staging action. `loaded`/`refreshing`
// let the drawer distinguish "no changes" from "haven't looked yet" — it must
// never flash the empty state over a summary that simply hasn't landed.
class DraftsStore {
	drafts = $state<{ project: string; draft: DraftView }[]>([]);
	// A summary has landed at least once this session (false again after sign-out).
	loaded = $state(false);
	// A refresh is in flight; the last good summary stays rendered meanwhile.
	refreshing = $state(false);

	readonly count = $derived(this.drafts.reduce((n, d) => n + d.draft.count, 0));
	// VMs with an unproposed staged change (this user's draft), keyed "ns/name".
	readonly stagedByKey = $derived.by(() => {
		const m = new Map<string, DraftItem>();
		for (const { draft } of this.drafts)
			for (const it of draft.items) m.set(`${it.namespace}/${it.name}`, it);
		return m;
	});

	async refresh() {
		// Platform authors also carry a platform-tier draft (cluster-scoped network +
		// namespace changes); draftsByProject drops it for non-authors (403 → skipped).
		const names = inventory.canManage
			? [...inventory.projectNames, PLATFORM_PROJECT]
			: inventory.projectNames;
		if (!names.length) {
			this.drafts = [];
			// An empty project set is a real answer once the inventory has landed —
			// only "inventory not streamed yet" still counts as not-looked.
			if (inventory.inventory) this.loaded = true;
			return;
		}
		this.refreshing = true;
		try {
			this.drafts = await draftsByProject(names);
			this.loaded = true;
		} catch {
			// Keep the last good summary; a 401 signs out centrally.
		} finally {
			this.refreshing = false;
		}
	}

	reset() {
		this.drafts = [];
		this.loaded = false;
		this.refreshing = false;
	}
}

export const drafts = new DraftsStore();

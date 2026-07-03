import { draftsByProject, type DraftItem, type DraftView } from '$lib/api';
import { inventory } from './inventory.svelte';

// The synthetic platform-tier project (matches the backend's platformProjectName);
// holds the cluster-scoped network + namespace changeset, proposable by authors.
export const PLATFORM_PROJECT = 'platform';

// Per-project drafts (the Changes panel + header badge). Refreshed when the set
// of projects or PR lanes changes (the layout's keyed effect) and directly after
// every staging action.
class DraftsStore {
	drafts = $state<{ project: string; draft: DraftView }[]>([]);

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
			return;
		}
		try {
			this.drafts = await draftsByProject(names);
		} catch {
			// Keep the last good summary; a 401 signs out centrally.
		}
	}

	reset() {
		this.drafts = [];
	}
}

export const drafts = new DraftsStore();

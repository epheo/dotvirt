import {
	api,
	Unauthorized,
	vmKey,
	type DraftItem,
	type DraftView,
	type PruneSet,
} from '#lib/api.js';
import { friendlyError } from '#lib/format.js';

// The synthetic platform-tier project (matches the backend's platformProjectName);
// holds the cluster-scoped network + namespace changeset, proposable by authors.
export const PLATFORM_PROJECT = 'platform';

// Everything a merge lets the sync delete: what the caller is shown, and what
// lies where they cannot see, which goes all the same.
export const pruneTotal = (d?: PruneSet | null) => (d?.prune?.length ?? 0) + (d?.pruneHidden ?? 0);

// The prune a yes was given to, as a stable string.
const pruneKey = (d?: PruneSet | null) =>
	`${(d?.prune ?? [])
		.map((r) => `${r.kind}/${r.namespace ?? ''}/${r.name}`)
		.sort()
		.join(',')}|${d?.pruneHidden ?? 0}`;

// The yes a propose or a revert needs while its merge prunes, held to the set
// it was given to: it drops the moment the shown set changes, keyed on a
// stable string, never per frame. Built during a component's init, as it owns
// an effect.
export class PruneYes {
	ack = $state(false);
	constructor(set: () => PruneSet | null | undefined) {
		const key = $derived(pruneKey(set()));
		$effect(() => {
			void key;
			this.ack = false;
		});
	}
}

// Per-project drafts (the Changes panel + header badge). Refreshed when the set
// of projects or PR lanes changes (the layout's keyed effect), when the Changes
// drawer opens, and directly after every staging action. `loaded`/`refreshing`
// let the drawer distinguish "no changes" from "haven't looked yet" - it must
// never flash the empty state over a summary that simply hasn't landed.
class DraftsStore {
	drafts = $state<{ project: string; draft: DraftView }[]>([]);
	// A summary has landed at least once this session (false again after sign-out).
	loaded = $state(false);
	// A refresh is in flight; the last good summary stays rendered meanwhile.
	refreshing = $state(false);
	// Projects whose draft could not be read on the last refresh.
	failed = $state<{ project: string; error: string }[]>([]);
	// Why the last refresh failed as a whole; the last good summary stays.
	error = $state('');

	readonly count = $derived(this.drafts.reduce((n, d) => n + d.draft.count, 0));
	// VMs with an unproposed staged change (this user's draft), keyed "ns/name".
	readonly stagedByKey = $derived.by(() => {
		const m = new Map<string, DraftItem>();
		for (const { draft } of this.drafts) for (const it of draft.items) m.set(vmKey(it), it);
		return m;
	});

	// Only the newest refresh, and nothing started before a reset, may write the
	// store. Refreshes overlap (the layout's keyed effect and a staging callback),
	// and an older response landing last would revert the summary; one landing
	// after sign-out would repopulate it.
	#gen = 0;

	async refresh() {
		const gen = ++this.#gen;
		this.refreshing = true;
		try {
			const d = await api.drafts();
			if (gen !== this.#gen) return;
			this.drafts = d.drafts;
			this.failed = d.failed;
			this.error = '';
			this.loaded = true;
		} catch (e) {
			// A 401 signs out centrally.
			if (gen === this.#gen && !(e instanceof Unauthorized)) this.error = friendlyError(e);
		} finally {
			if (gen === this.#gen) this.refreshing = false;
		}
	}

	reset() {
		this.#gen++;
		this.drafts = [];
		this.failed = [];
		this.error = '';
		this.loaded = false;
		this.refreshing = false;
	}
}

export const drafts = new DraftsStore();

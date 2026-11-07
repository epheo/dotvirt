import { api, type Proposal, type TaskEntry } from '#lib/api.js';
import { follow, followKey, type Followed } from '#lib/tray.js';
import { persisted } from './persisted.svelte';

// How often a merged PR whose commit is not in history yet asks again.
const RESOLVE_EVERY_MS = 30_000;

// The change tray's memory: the PRs this user opened, followed until they are
// live or dismissed, persisted so a reload keeps following and cleared on
// sign-out so the next user starts clean; and whether it is unfolded.
class TrayStore {
	// Folded on every load, like the task dock: the tray unfolds only when
	// the user asks; the pill never hides a staged draft.
	#open = $state(false);
	#followed = persisted<Followed[]>('dotvirt.tray.followed', []);
	#asked = new Map<string, number>();

	get open(): boolean {
		return this.#open;
	}
	set open(v: boolean) {
		this.#open = v;
	}
	get followed(): Followed[] {
		return this.#followed.value;
	}

	// Called when the open PRs or the merge feed move.
	update(proposals: Proposal[], feed: TaskEntry[]) {
		const merges = feed.filter((t) => t.kind === 'merge');
		const next = follow(this.#followed.value, proposals, merges);
		if (next !== this.#followed.value) this.#followed.value = next;
		for (const f of next) if (f.mergedAt && !f.hash) void this.#resolve(f);
	}

	// The merge commit is what Argo's revision is checked against; history is
	// read under the caller's token, once per merge until it shows up.
	async #resolve(f: Followed) {
		const key = followKey(f.project, f.prNumber);
		const last = this.#asked.get(key) ?? 0;
		if (Date.now() - last < RESOLVE_EVERY_MS) return;
		this.#asked.set(key, Date.now());
		try {
			const c = (await api.history(f.project)).find((c) => c.prNumber === f.prNumber);
			if (!c) return;
			this.#followed.value = this.#followed.value.map((x) =>
				followKey(x.project, x.prNumber) === key ? { ...x, hash: c.hash } : x,
			);
		} catch {
			// History unreadable for now: the lane stays applying and asks again.
		}
	}

	// Called when the review sheet opens a PR: the tray follows it from now,
	// with what it declares, before the open list catches up.
	remember(f: Followed) {
		const key = followKey(f.project, f.prNumber);
		const have = this.#followed.value.some((x) => followKey(x.project, x.prNumber) === key);
		this.#followed.value = have
			? this.#followed.value.map((x) =>
					followKey(x.project, x.prNumber) === key ? { ...x, ...f } : x,
				)
			: [...this.#followed.value, f];
	}

	dismiss(key: string) {
		this.#followed.value = this.#followed.value.filter(
			(f) => followKey(f.project, f.prNumber) !== key,
		);
	}

	reset() {
		this.#followed.value = [];
		this.#asked.clear();
	}
}

export const tray = new TrayStore();

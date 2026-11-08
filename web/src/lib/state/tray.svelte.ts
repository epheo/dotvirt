import { api, HttpError, type Proposal, type TaskEntry } from '#lib/api.js';
import { follow, followKey, type Followed } from '#lib/tray.js';
import { persisted } from './persisted.svelte';

// How often a project's history is read again while a followed merge waits
// on it: its commit not in history yet, the read failed, or Argo runs a
// revision newer than the last read.
const RESOLVE_EVERY_MS = 30_000;

// The change tray's memory: the PRs this user opened, followed until they are
// live or dismissed, persisted so a reload keeps following and cleared on
// sign-out so the next user starts clean; and whether it is unfolded.
class TrayStore {
	// Folded on every load, like the task dock: the tray unfolds only when
	// the user asks; the pill never hides a staged draft.
	#open = $state(false);
	#followed = persisted<Followed[]>('dotvirt.tray.followed', []);
	// Each project's history as last read, newest first: what tells whether a
	// later revision holds a followed merge.
	#commits = $state<Record<string, string[]>>({});
	#readAt = new Map<string, number>();
	#timers = new Map<string, ReturnType<typeof setTimeout>>();
	// Projects whose history answered 403 (a platform stager without platform
	// read): no later read can find their merges, so none is made.
	#denied = new Set<string>();

	get open(): boolean {
		return this.#open;
	}
	set open(v: boolean) {
		this.#open = v;
	}
	get followed(): Followed[] {
		return this.#followed.value;
	}
	commits(project: string): string[] | undefined {
		return this.#commits[project];
	}

	// Called when the open PRs or the merge feed move.
	update(proposals: Proposal[], feed: TaskEntry[]) {
		const merges = feed.filter((t) => t.kind === 'merge');
		const next = follow(this.#followed.value, proposals, merges);
		if (next !== this.#followed.value) this.#followed.value = next;
		for (const p of new Set(next.filter((f) => f.mergedAt).map((f) => f.project))) this.want(p);
	}

	// want reads a project's history now, or once the last read is old enough.
	// Nothing else may ask again: the stream can stay quiet for as long as a
	// merge waits, so a skipped read becomes a timer, never a drop.
	want(project: string) {
		if (this.#timers.has(project) || this.#denied.has(project)) return;
		const wait = (this.#readAt.get(project) ?? -Infinity) + RESOLVE_EVERY_MS - Date.now();
		if (wait <= 0) {
			void this.#read(project);
			return;
		}
		this.#timers.set(
			project,
			setTimeout(() => {
				this.#timers.delete(project);
				void this.#read(project);
			}, wait),
		);
	}

	// The merge commit is what Argo's revision is checked against; history is
	// read under the caller's token until every followed merge shows up in it.
	async #read(project: string) {
		this.#readAt.set(project, Date.now());
		let waiting = true;
		try {
			const history = await api.history(project);
			this.#commits = { ...this.#commits, [project]: history.map((c) => c.hash) };
			// Newest first: a PR's merge commit is its first mention.
			const hashOf = new Map<number, string>();
			for (const c of history)
				if (c.prNumber && !hashOf.has(c.prNumber)) hashOf.set(c.prNumber, c.hash);
			let found = false;
			const next = this.#followed.value.map((f) => {
				const hash = f.project === project && f.mergedAt && !f.hash && hashOf.get(f.prNumber);
				if (!hash) return f;
				found = true;
				return { ...f, hash };
			});
			if (found) this.#followed.value = next;
			waiting = next.some((f) => f.project === project && f.mergedAt && !f.hash);
		} catch (e) {
			// History unreadable for now: the lane stays applying and asks again.
			if (e instanceof HttpError && e.status === 403) {
				this.#denied.add(project);
				waiting = false;
			}
		}
		if (waiting) this.want(project);
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
		this.#commits = {};
		this.#readAt.clear();
		this.#denied.clear();
		for (const t of this.#timers.values()) clearTimeout(t);
		this.#timers.clear();
	}
}

export const tray = new TrayStore();

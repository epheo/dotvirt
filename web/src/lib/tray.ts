import type { Change, DraftItem, DraftView, Proposal, ProjectSync } from '#lib/api.js';
import { opStands } from '#lib/gitops.js';

// A PR this user opened, followed past its review: the open list drops it on
// merge and on close alike, so the tray keeps its own record. mergedAt comes
// from the merge feed; hash, from the project's history, is what the Argo
// revision is compared against to call the merge live.
export type Followed = {
	project: string;
	prNumber: number;
	title: string;
	prURL: string;
	goneAt?: number; // left the open list, merge not seen yet
	mergedAt?: number;
	hash?: string;
	// What the PR declares and which VMs restart, kept from the review sheet;
	// absent for a PR opened elsewhere.
	lines?: string[];
	restarts?: string[];
};

export type TrayLane =
	| { kind: 'staged'; key: string; project: string; draft: DraftView; others: Proposal[] }
	| { kind: 'review'; key: string; project: string; pr: Proposal }
	| {
			kind: 'applying' | 'live' | 'refused' | 'merged';
			key: string;
			project: string;
			pr: Followed;
			error?: string;
			// Argo runs the merge commit; applying is then the sync itself.
			picked?: boolean;
	  };

export const followKey = (project: string, prNumber: number) => `${project}#${prNumber}`;

// How a merge stands on its project: live once Argo runs a revision that holds
// the merge commit and the app is Synced, refused while a failed operation
// stands, applying in between, and plain merged when no Argo watches. A later
// merge on top still holds this one: commits, the project's history newest
// first, orders the two. Without it only the merge commit itself counts.
export type MergeState = { kind: 'merged' | 'refused' | 'live' | 'applying'; picked: boolean };

export function mergeState(
	g: ProjectSync | undefined,
	hash: string | undefined,
	commits?: readonly string[],
): MergeState {
	if (!g) return { kind: 'merged', picked: false };
	const picked = !!hash && !!g.revision && holds(g.revision, hash, commits);
	if (picked && g.sync === 'Synced') return { kind: 'live', picked };
	if (opStands(g)) return { kind: 'refused', picked };
	return { kind: 'applying', picked };
}

function holds(revision: string, hash: string, commits?: readonly string[]): boolean {
	if (hash.startsWith(revision)) return true;
	const r = commits?.findIndex((c) => c.startsWith(revision)) ?? -1;
	return r >= 0 && commits!.indexOf(hash) > r;
}

// Every change of this user's in flight, one lane each, in pipeline order:
// staged drafts, PRs in review, merges on their way to the cluster, each
// placed by mergeState.
export function trayLanes(src: {
	drafts: { project: string; draft: DraftView }[];
	proposals: Proposal[];
	followed: Followed[];
	sync: (project: string) => ProjectSync | undefined;
	commits?: (project: string) => readonly string[] | undefined;
}): TrayLane[] {
	const out: TrayLane[] = [];
	for (const { project, draft } of src.drafts)
		if (draft.count > 0)
			out.push({
				kind: 'staged',
				key: `staged:${project}`,
				project,
				draft,
				others: src.proposals.filter((p) => p.project === project && !p.mine),
			});
	for (const pr of src.proposals)
		if (pr.mine)
			out.push({
				kind: 'review',
				key: followKey(pr.project, pr.prNumber),
				project: pr.project,
				pr,
			});
	for (const f of src.followed) {
		const key = followKey(f.project, f.prNumber);
		if (!f.mergedAt) continue;
		const g = src.sync(f.project);
		const lane = { key, project: f.project, pr: f };
		const m = mergeState(g, f.hash, src.commits?.(f.project));
		if (m.kind === 'refused') out.push({ kind: 'refused', ...lane, error: g?.syncError });
		else if (m.kind === 'applying') out.push({ kind: 'applying', ...lane, picked: m.picked });
		else out.push({ kind: m.kind, ...lane });
	}
	return out;
}

// A PR that left the open list without a merge in the feed for this long was
// closed, not merged: the tray lets it go without claiming either.
const CLOSE_GRACE_MS = 5 * 60 * 1000;

// Follow this user's open PRs and settle the ones that left review: a merge in
// the feed marks them merged, a long silence drops them. Pure: returns the
// next list, or the same list when nothing moved.
export function follow(
	followed: Followed[],
	proposals: Proposal[],
	merges: { project?: string; prNumber?: number; at: string }[],
	now = Date.now(),
): Followed[] {
	const open = new Set(proposals.map((p) => followKey(p.project, p.prNumber)));
	let changed = false;
	const next: Followed[] = [];
	for (const f of followed) {
		const key = followKey(f.project, f.prNumber);
		if (open.has(key) && f.goneAt) {
			// Back in the open list: the grace restarts the next time it leaves.
			const { goneAt: _, ...rest } = f;
			next.push(rest);
			changed = true;
			continue;
		}
		if (open.has(key) || f.mergedAt) {
			next.push(f);
			continue;
		}
		const m = merges.find((t) => t.project === f.project && t.prNumber === f.prNumber);
		if (m) {
			next.push({ ...f, mergedAt: Date.parse(m.at) });
			changed = true;
		} else if (!f.goneAt) {
			next.push({ ...f, goneAt: now });
			changed = true;
		} else if (now - f.goneAt < CLOSE_GRACE_MS) next.push(f);
		else changed = true;
	}
	for (const p of proposals)
		if (p.mine && !followed.some((f) => f.project === p.project && f.prNumber === p.prNumber)) {
			next.push({ project: p.project, prNumber: p.prNumber, title: p.title ?? '', prURL: p.prURL });
			changed = true;
		}
	return changed ? next : followed;
}

// One line per object a PR declares, as the tray says it once live:
// "web-1: CPU 4, Memory 8Gi". Restarts names the VMs the merge restarts.
export function changeSummary(items: DraftItem[]): { lines: string[]; restarts: string[] } {
	return {
		lines: items.map((it) =>
			it.kind === 'delete'
				? `${it.name} deleted`
				: it.kind === 'create'
					? `${it.name} created`
					: `${it.name}: ${it.changes
							.map((c) => (c.action === 'remove' ? `${c.field} removed` : `${c.field} ${c.to}`))
							.join(', ')}`,
		),
		restarts: items.filter((it) => it.changes.some((c) => c.restart)).map((it) => it.name),
	};
}

// A change in a few words, the way the tray and the tables list it.
const changeLine = (c: Change) =>
	c.action === 'change'
		? `${c.field.toLowerCase()} ${c.from} to ${c.to}`
		: c.action === 'add'
			? `${c.field.toLowerCase()}${c.to ? ` ${c.to}` : ''}`
			: `remove ${c.field.toLowerCase()}`;

export const itemLine = (it: DraftItem) =>
	it.kind === 'delete'
		? 'delete'
		: it.changes.slice(0, 2).map(changeLine).join(', ') +
			(it.changes.length > 2 ? `, +${it.changes.length - 2} more` : '');

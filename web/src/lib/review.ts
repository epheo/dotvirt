// The Changes pane's selection as URL state, so a review is shareable and other
// views deep-link into it: ?project= alone opens a project's history, with
// &commit= (a full hash), &pr= or &item= selecting what the pane reviews.

export type ReviewSel =
	| { kind: 'item'; project: string; key: string }
	| { kind: 'proposal'; project: string; prNumber: number }
	| { kind: 'commit'; project: string; hash: string };

export type ReviewTarget = ReviewSel | { kind: 'history'; project: string };

const fullHash = /^[0-9a-f]{40}$/;

// A staged item's identity within its project's draft (the &item= value).
export const itemKey = (it: { resource?: string; namespace: string; name: string }) =>
	`${it.resource || 'vm'}:${it.namespace}/${it.name}`;

export function reviewURL(t: ReviewTarget | null): string {
	if (!t) return '/changes';
	const q = new URLSearchParams({ project: t.project });
	if (t.kind === 'commit') q.set('commit', t.hash);
	else if (t.kind === 'proposal') q.set('pr', String(t.prNumber));
	else if (t.kind === 'item') q.set('item', t.key);
	return `/changes?${q}`;
}

export function parseReview(q: URLSearchParams): ReviewSel | null {
	const project = q.get('project');
	if (!project) return null;
	const commit = q.get('commit');
	if (commit && fullHash.test(commit)) return { kind: 'commit', project, hash: commit };
	const pr = Number(q.get('pr'));
	if (Number.isInteger(pr) && pr > 0) return { kind: 'proposal', project, prNumber: pr };
	const item = q.get('item');
	if (item) return { kind: 'item', project, key: item };
	return null;
}

export function sameReview(a: ReviewSel | null, b: ReviewSel | null): boolean {
	return reviewURL(a) === reviewURL(b);
}

import { describe, expect, it } from 'vitest';
import type { DraftView, Proposal, ProjectSync } from '#lib/api.js';
import { follow, trayLanes, type Followed } from '#lib/tray.js';

const draft = (count: number): DraftView => ({ base: 'main', branch: 'b', count, items: [] });
const pr = (over: Partial<Proposal>): Proposal => ({
	project: 'web',
	prNumber: 43,
	prURL: 'https://forge/pr/43',
	title: 'web-1: 8Gi',
	mine: true,
	...over,
});
const merged: Followed = {
	project: 'web',
	prNumber: 43,
	title: 'web-1: 8Gi',
	prURL: 'https://forge/pr/43',
	mergedAt: 1,
	hash: 'abc1234def',
};

function lanes(over: Partial<Parameters<typeof trayLanes>[0]> = {}, sync?: ProjectSync) {
	return trayLanes({
		drafts: [],
		proposals: [],
		followed: [],
		sync: () => sync,
		...over,
	}).map((l) => l.kind);
}

describe('trayLanes', () => {
	it('orders a project draft, then my PRs; others are context on the draft', () => {
		const l = trayLanes({
			drafts: [
				{ project: 'web', draft: draft(2) },
				{ project: 'db', draft: draft(0) },
			],
			proposals: [pr({}), pr({ prNumber: 42, mine: false, by: 'bob' })],
			followed: [],
			sync: () => undefined,
		});
		expect(l.map((x) => x.kind)).toEqual(['staged', 'review']);
		expect(l[0].kind === 'staged' && l[0].others.map((p) => p.prNumber)).toEqual([42]);
	});

	it('calls a merge live only once Argo runs the merge commit, Synced', () => {
		const at = (g: ProjectSync) => lanes({ followed: [merged] }, g);
		expect(at({ sync: 'Synced', revision: '9999999' })).toEqual(['applying']);
		expect(at({ sync: 'OutOfSync', revision: 'abc1234' })).toEqual(['applying']);
		expect(at({ sync: 'Synced', revision: 'abc1234' })).toEqual(['live']);
		expect(at({ sync: 'OutOfSync', operation: 'Failed', syncError: 'denied' })).toEqual([
			'refused',
		]);
		expect(lanes({ followed: [merged] })).toEqual(['merged']);
	});
});

describe('follow', () => {
	it('picks up my open PRs and marks a merged one from the feed', () => {
		const f1 = follow([], [pr({}), pr({ prNumber: 42, mine: false })], []);
		expect(f1.map((f) => f.prNumber)).toEqual([43]);
		const f2 = follow(f1, [], [{ project: 'web', prNumber: 43, at: '2026-11-06T10:00:00Z' }]);
		expect(f2[0].mergedAt).toBe(Date.parse('2026-11-06T10:00:00Z'));
	});

	it('lets a PR go quietly when it closes without a merge', () => {
		const f1 = follow([], [pr({})], []);
		const gone = follow(f1, [], [], 1000);
		expect(gone[0].goneAt).toBe(1000);
		expect(follow(gone, [], [], 1000 + 60_000)).toHaveLength(1);
		expect(follow(gone, [], [], 1000 + 6 * 60_000)).toHaveLength(0);
	});

	it('returns the same list when nothing moved', () => {
		const f1 = follow([], [pr({})], []);
		expect(follow(f1, [pr({})], [])).toBe(f1);
	});
});

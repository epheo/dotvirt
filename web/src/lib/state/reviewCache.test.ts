import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Inventory, ProposalDetail } from '#lib/api.js';

// A PR's review is read once per head: the lane reports the head the review
// renders, so a push to the PR is a miss, and a frame that moves nothing else
// is a hit.

const api = vi.hoisted(() => ({
	api: { proposal: vi.fn(), commit: vi.fn() },
	Unauthorized: class extends Error {},
	HttpError: class extends Error {
		constructor(
			message: string,
			readonly status: number,
		) {
			super(message);
		}
	},
	withRetry: vi.fn(),
	vmKey: (v: { namespace: string; name: string }) => `${v.namespace}/${v.name}`,
}));
vi.mock('#lib/api.js', () => api);

import { inventory } from '#lib/state/inventory.svelte.js';
import { reviewCache } from '#lib/state/reviewCache.svelte.js';

const lane = (head: string, title = 'grow db') =>
	({
		projects: [],
		proposals: [{ project: 'p', prNumber: 4, prURL: 'http://forge/pulls/4', title, head }],
	}) as unknown as Inventory;

const detail = (name: string): ProposalDetail =>
	({
		proposal: { project: 'p', prNumber: 4, prURL: '' },
		items: [{ kind: 'edit', namespace: 'web', name, changes: [] }],
	}) as unknown as ProposalDetail;

const settle = () => new Promise((r) => setTimeout(r));

describe('reviewCache proposals', () => {
	beforeEach(() => {
		reviewCache.reset();
		api.api.proposal.mockReset();
	});

	it('reads a PR once per head', async () => {
		inventory.apply(lane('aaa'));
		api.api.proposal.mockResolvedValueOnce(detail('db'));
		reviewCache.loadProposal('p', 4);
		await settle();
		expect(reviewCache.proposal('p', 4)?.data?.items[0].name).toBe('db');

		inventory.apply(lane('aaa', 'retitled'));
		reviewCache.loadProposal('p', 4);
		expect(api.api.proposal).toHaveBeenCalledTimes(1);

		inventory.apply(lane('bbb'));
		expect(reviewCache.proposal('p', 4)).toBeUndefined();
		api.api.proposal.mockResolvedValueOnce(detail('cache'));
		reviewCache.loadProposal('p', 4);
		await settle();
		expect(api.api.proposal).toHaveBeenCalledTimes(2);
		expect(reviewCache.proposal('p', 4)?.data?.items[0].name).toBe('cache');
	});

	it('the PR set key moves with a head', () => {
		inventory.apply(lane('aaa'));
		const before = inventory.proposalsKey;
		inventory.apply(lane('aaa', 'retitled'));
		expect(inventory.proposalsKey).toBe(before);
		inventory.apply(lane('bbb'));
		expect(inventory.proposalsKey).not.toBe(before);
	});

	it('a 403 is the answer, not a failure to retry', async () => {
		inventory.apply(lane('aaa'));
		api.api.proposal.mockRejectedValueOnce(new api.HttpError('changed outside the console', 403));
		reviewCache.loadProposal('p', 4);
		await settle();
		expect(reviewCache.proposal('p', 4)).toMatchObject({
			error: 'changed outside the console',
			denied: true,
		});

		api.api.proposal.mockRejectedValueOnce(new api.HttpError('forge down', 503));
		reviewCache.loadProposal('p', 4, true);
		await settle();
		expect(reviewCache.proposal('p', 4)?.denied).toBe(false);
	});
});

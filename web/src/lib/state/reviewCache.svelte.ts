import { untrack } from 'svelte';
import { api, type CommitDetail, type Proposal, type ProposalDetail } from '#lib/api.js';
import { friendlyError } from '#lib/format.js';
import { commitKey, prKey } from '#lib/review.js';
import { itemLine } from '#lib/tray.js';
import { inventory } from './inventory.svelte';

// A review is a past commit's or an open PR's items: the same field diff a
// staged item shows. The Changes panes, the VM page's Changes tab and every
// object's History card look at the same ones, so a review loads once per
// key for the session. A commit never changes; a PR's review is keyed on the
// head the live lane reports, so a push to the PR, from this app or not, is
// a miss that loads the new version.
type Review<T> = { readonly data: T | null; readonly error: string };

class ReviewCache {
	#commits = $state<Record<string, Review<CommitDetail>>>({});
	#proposals = $state<Record<string, Review<ProposalDetail>>>({});
	// Bumped by reset(), so a load started before a sign-out drops its answer.
	#generation = 0;
	// Each open PR's head, rebuilt when the lane's PR set or a head moves,
	// never per frame.
	#heads = $derived.by(() => {
		inventory.proposalsKey;
		return untrack(
			() => new Map(inventory.proposals.map((p) => [prKey(p.project, p.prNumber), p.head ?? ''])),
		);
	});
	#reviewKey(project: string, prNumber: number) {
		const pr = prKey(project, prNumber);
		return `${pr}#${this.#heads.get(pr) ?? ''}`;
	}

	commit(project: string, hash: string): Review<CommitDetail> | undefined {
		return this.#commits[commitKey(project, hash)];
	}
	proposal(project: string, prNumber: number): Review<ProposalDetail> | undefined {
		return this.#proposals[this.#reviewKey(project, prNumber)];
	}

	// The loads are idempotent writes: call them from an effect or a handler,
	// never from a derived.
	loadCommit(project: string, hash: string) {
		this.#fill(this.#commits, commitKey(project, hash), () => api.commit(project, hash));
	}
	// retry asks again after a failure: the head branch is read from the
	// mirror, so a PR pushed moments ago can answer not-yet.
	loadProposal(project: string, prNumber: number, retry = false) {
		const key = this.#reviewKey(project, prNumber);
		// The PR's earlier heads are never read again.
		const pr = `${prKey(project, prNumber)}#`;
		untrack(() => {
			for (const k of Object.keys(this.#proposals))
				if (k.startsWith(pr) && k !== key) delete this.#proposals[k];
		});
		this.#fill(this.#proposals, key, () => api.proposal(project, prNumber), retry);
	}
	loadProposals(proposals: Proposal[]) {
		for (const p of proposals) this.loadProposal(p.project, p.prNumber);
	}

	// Open PRs whose diff names a VM, by "ns/name", each with its change in a
	// few words. Reads the reviews loaded so far: a derived, never a load.
	vmPRs(proposals: Proposal[]): Map<string, { pr: Proposal; line: string }[]> {
		const m = new Map<string, { pr: Proposal; line: string }[]>();
		for (const pr of proposals)
			for (const it of this.proposal(pr.project, pr.prNumber)?.data?.items ?? []) {
				if (it.resource && it.resource !== 'vm') continue;
				const k = `${it.namespace}/${it.name}`;
				const line = it.kind === 'create' ? 'create' : itemLine(it);
				m.set(k, [...(m.get(k) ?? []), { pr, line }]);
			}
		return m;
	}

	forgetProposals(project: string) {
		for (const key of Object.keys(this.#proposals))
			if (key.startsWith(`${project}#`)) delete this.#proposals[key];
	}

	reset() {
		this.#generation++;
		this.#commits = {};
		this.#proposals = {};
	}

	#fill<T>(store: Record<string, Review<T>>, key: string, load: () => Promise<T>, retry = false) {
		untrack(() => {
			const have = store[key];
			if (have && !(retry && have.error)) return;
			const gen = this.#generation;
			store[key] = { data: null, error: '' };
			load().then(
				(data) => {
					if (gen === this.#generation) store[key] = { data, error: '' };
				},
				(e) => {
					if (gen === this.#generation) store[key] = { data: null, error: friendlyError(e) };
				},
			);
		});
	}
}

export const reviewCache = new ReviewCache();

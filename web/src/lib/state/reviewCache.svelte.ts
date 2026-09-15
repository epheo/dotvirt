import { untrack } from 'svelte';
import { api, type CommitDetail, type ProposalDetail } from '$lib/api';
import { friendlyError } from '$lib/format';
import { commitKey, prKey } from '$lib/review';

// A review is a past commit's or an open PR's items: the same field diff a
// staged item shows. The Changes panes, the VM page's Changes tab and every
// object's History card look at the same ones, so a review loads once per
// key for the session. A commit never changes; a PR's head moves only when
// the app proposes again, which forgets that project's PR reviews.
type Review<T> = { readonly data: T | null; readonly error: string };

class ReviewCache {
	#commits = $state<Record<string, Review<CommitDetail>>>({});
	#proposals = $state<Record<string, Review<ProposalDetail>>>({});
	// Bumped by reset(), so a load started before a sign-out drops its answer.
	#generation = 0;

	commit(project: string, hash: string): Review<CommitDetail> | undefined {
		return this.#commits[commitKey(project, hash)];
	}
	proposal(project: string, prNumber: number): Review<ProposalDetail> | undefined {
		return this.#proposals[prKey(project, prNumber)];
	}

	// The loads are idempotent writes: call them from an effect or a handler,
	// never from a derived.
	loadCommit(project: string, hash: string) {
		this.#fill(this.#commits, commitKey(project, hash), () => api.commit(project, hash));
	}
	// retry asks again after a failure: the head branch is read from the
	// mirror, so a PR pushed moments ago can answer not-yet.
	loadProposal(project: string, prNumber: number, retry = false) {
		this.#fill(
			this.#proposals,
			prKey(project, prNumber),
			() => api.proposal(project, prNumber),
			retry,
		);
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

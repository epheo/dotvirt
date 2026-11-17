import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';
import type { DraftView, PruneSet } from '#lib/api.js';
import { drafts } from '#lib/state/drafts.svelte.js';
import UndoModal from './UndoModal.svelte';

// A revert's merge prunes like any other: what reverting lets ArgoCD delete
// beyond its own restores needs the same explicit yes, read for this commit
// (never the draft's set), and asked again once a refusal shows it moved.

const hash = 'a'.repeat(40);
const oldVM = { kind: 'VirtualMachine', namespace: 'db-prod', name: 'old-vm' };
const first: PruneSet = { prune: [oldVM], pruneHidden: 1 };
const grown: PruneSet = { prune: [oldVM], pruneHidden: 2 };

let pending: PruneSet;
let revert: () => Response;
let fetchMock: ReturnType<typeof vi.fn>;
beforeEach(() => {
	pending = first;
	revert = () => new Response(JSON.stringify({ prNumber: 9 }), { status: 200 });
	fetchMock = vi.fn(async (url: string) =>
		url.endsWith(`/history/${hash}/prune`)
			? new Response(JSON.stringify(pending), { status: 200 })
			: url.endsWith('/revert')
				? revert()
				: new Response(null, { status: 204 }),
	);
	vi.stubGlobal('fetch', fetchMock);
});
afterEach(() => {
	vi.unstubAllGlobals();
	drafts.reset();
});

function open() {
	// The draft's own lane is no part of the revert: it must not be what is asked.
	const lane: DraftView = {
		base: 'main',
		branch: 'd',
		count: 0,
		items: [],
		prune: [],
		pruneHidden: 0,
	};
	drafts.drafts = [{ project: 'team-db', draft: lane }];
	render(UndoModal, { project: 'team-db', hash, onclose: vi.fn(), onreverted: vi.fn() });
}

const sent = () => {
	const [, init] = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/revert'))!;
	return JSON.parse(init.body);
};

describe('UndoModal', () => {
	it("holds the revert behind the yes to the commit's own set and sends it", async () => {
		open();
		const yes = await screen.findByLabelText(/Delete these 2 when this merges/);
		const button = screen.getByRole('button', { name: 'Open pull request' }) as HTMLButtonElement;
		expect(button.disabled).toBe(true);
		expect(screen.getByText(/1 more outside your view/)).toBeTruthy();
		await fireEvent.click(yes);
		expect(button.disabled).toBe(false);
		await fireEvent.click(button);
		expect(sent()).toMatchObject({ hash, prune: [oldVM], pruneHidden: 1 });
	});

	it('a refusal re-reads the set and asks again when it grew', async () => {
		open();
		await fireEvent.click(await screen.findByLabelText(/Delete these 2 when this merges/));
		pending = grown;
		revert = () =>
			new Response('conflict: merging this now deletes objects the review did not list', {
				status: 409,
			});
		await fireEvent.click(screen.getByRole('button', { name: 'Open pull request' }));
		const again = (await screen.findByLabelText(
			/Delete these 3 when this merges/,
		)) as HTMLInputElement;
		expect(again.checked).toBe(false);
	});
});

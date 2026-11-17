import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';
import type { DraftView } from '#lib/api.js';
import { drafts } from '#lib/state/drafts.svelte.js';
import UndoModal from './UndoModal.svelte';

// A revert's merge prunes like any other: what the project's lane says ArgoCD
// would delete needs the same explicit yes before the pull request opens.

const lane: DraftView = {
	base: 'main',
	branch: 'draft',
	count: 0,
	items: [],
	prune: [{ kind: 'VirtualMachine', namespace: 'db-prod', name: 'old-vm' }],
	pruneHidden: 1,
};

let fetchMock: ReturnType<typeof vi.fn>;
beforeEach(() => {
	fetchMock = vi.fn(async () => new Response(JSON.stringify({ prNumber: 9 }), { status: 200 }));
	vi.stubGlobal('fetch', fetchMock);
});
afterEach(() => {
	vi.unstubAllGlobals();
	drafts.reset();
});

describe('UndoModal', () => {
	it('holds the revert behind the deletion yes and sends what was acknowledged', async () => {
		drafts.drafts = [{ project: 'team-db', draft: lane }];
		render(UndoModal, {
			project: 'team-db',
			hash: 'a'.repeat(40),
			onclose: vi.fn(),
			onreverted: vi.fn(),
		});
		const open = screen.getByRole('button', { name: 'Open pull request' }) as HTMLButtonElement;
		expect(open.disabled).toBe(true);
		expect(screen.getByText(/1 more outside your view/)).toBeTruthy();
		await fireEvent.click(screen.getByLabelText(/Delete these 2 when this merges/));
		expect(open.disabled).toBe(false);
		await fireEvent.click(open);
		const [, init] = fetchMock.mock.calls.find(([url]) => String(url).includes('/revert'))!;
		expect(JSON.parse(init.body)).toMatchObject({
			hash: 'a'.repeat(40),
			prune: lane.prune,
			pruneHidden: 1,
		});
	});
});

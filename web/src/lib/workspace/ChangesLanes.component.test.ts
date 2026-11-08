import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/svelte';
import type { DraftView } from '#lib/api.js';
import { ui } from '#lib/state/ui.svelte.js';
import ChangesLanes from './ChangesLanes.svelte';

// Discarding a project's whole draft is one click from losing unproposed
// work: it asks first, in its own lane, and says when it is done.

const draft = (count: number): DraftView => ({
	base: 'main',
	branch: 'draft',
	count,
	items: Array.from({ length: count }, (_, i) => ({
		kind: 'edit' as const,
		namespace: 'team-a',
		name: `vm-${i}`,
		changes: [],
	})),
});

function setup() {
	return render(ChangesLanes, {
		scopeProject: '',
		lanes: [
			{ project: 'alpha', draft: draft(2) },
			{ project: 'beta', draft: draft(1) },
		],
		proposedLane: [],
		visibleProposals: [],
		mineOnly: { value: false },
		results: {},
		history: {},
		repoProjects: [],
		active: null,
		onselect: vi.fn(),
		ontogglehistory: vi.fn(),
		onretryhistory: vi.fn(),
		ondismiss: vi.fn(),
	});
}

let fetchMock: ReturnType<typeof vi.fn>;
beforeEach(() => {
	ui.reset();
	fetchMock = vi.fn(async () => new Response(null, { status: 204 }));
	vi.stubGlobal('fetch', fetchMock);
});
afterEach(() => vi.unstubAllGlobals());

const discards = () => fetchMock.mock.calls.filter(([, init]) => init?.method === 'DELETE');

describe('ChangesLanes discard all', () => {
	it('asks before discarding, then reports it', async () => {
		setup();
		await fireEvent.click(screen.getAllByRole('button', { name: 'discard all' })[0]);
		expect(discards()).toHaveLength(0);
		expect(screen.getByText('Discard 2 staged changes?')).toBeTruthy();

		await fireEvent.click(screen.getByRole('button', { name: 'Discard' }));
		await vi.waitFor(() =>
			expect(ui.toasts.map((t) => t.msg)).toContain('Discarded the staged changes in alpha.'),
		);
		expect(discards().map(([url]) => url)).toEqual(['/api/draft?project=alpha']);
		expect(screen.queryByText('Discard 2 staged changes?')).toBeNull();
	});

	it('keeps the draft on Keep', async () => {
		setup();
		await fireEvent.click(screen.getAllByRole('button', { name: 'discard all' })[1]);
		await fireEvent.click(screen.getByRole('button', { name: 'Keep' }));
		expect(discards()).toHaveLength(0);
		expect(screen.getAllByRole('button', { name: 'discard all' })).toHaveLength(2);
	});

	it('a discard in flight is busy in its own lane only', async () => {
		let answer!: (r: Response) => void;
		fetchMock.mockImplementationOnce(() => new Promise<Response>((r) => (answer = r)));
		setup();
		const [alpha, beta] = screen.getAllByRole('button', { name: 'discard all' });
		await fireEvent.click(alpha);
		await fireEvent.click(beta);
		const [discardAlpha, discardBeta] = screen.getAllByRole('button', { name: 'Discard' });
		await fireEvent.click(discardAlpha);

		const busy = screen.getByRole('button', { name: 'Discarding...' });
		expect((busy as HTMLButtonElement).disabled).toBe(true);
		expect((discardBeta as HTMLButtonElement).disabled).toBe(false);
		expect(within(discardBeta.parentElement!).getByText('Discard 1 staged change?')).toBeTruthy();
		answer(new Response(null, { status: 204 }));
	});
});

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';
import type { DraftView, Inventory } from '#lib/api.js';
import { drafts } from '#lib/state/drafts.svelte.js';
import { inventory } from '#lib/state/inventory.svelte.js';
import { ui } from '#lib/state/ui.svelte.js';
import ProposeSheet from './ProposeSheet.svelte';

// The sheet says what a propose will do: open a PR, or add to the user's
// open one (whose title and text are not the draft's defaults), and says a
// refusal where the button is, with the way forward.

const draft: DraftView = {
	base: 'main',
	branch: 'draft',
	count: 1,
	items: [
		{
			kind: 'edit',
			namespace: 'db-prod',
			name: 'db-1',
			changes: [{ field: 'Labels', action: 'add', to: 'tier=gold' }],
		},
	],
	defaultTitle: 'Update db-1: Labels',
	defaultBody: '## Changes\n- Labels: + tier=gold\n',
};

const openPR = {
	project: 'team-db',
	prNumber: 41,
	prURL: 'https://forge.example/team-db/pulls/41',
	title: 'db-1: raise memory to 12Gi',
	mine: true,
};

function setup(proposals: object[]) {
	inventory.apply({ projects: [], proposals } as unknown as Inventory);
	drafts.drafts = [{ project: 'team-db', draft }];
	return render(ProposeSheet, { project: 'team-db', onclose: vi.fn() });
}

let fetchMock: ReturnType<typeof vi.fn>;
beforeEach(() => {
	ui.reset();
	fetchMock = vi.fn(async () => new Response(null, { status: 204 }));
	vi.stubGlobal('fetch', fetchMock);
});
afterEach(() => {
	vi.unstubAllGlobals();
	drafts.reset();
});

const refuse = (status: number, text: string) =>
	fetchMock.mockImplementation(async (url: string) =>
		url.startsWith('/api/draft/propose')
			? new Response(text, { status })
			: new Response(null, { status: 204 }),
	);

describe('ProposeSheet', () => {
	it('without an open PR, offers the draft defaults and opens one', () => {
		setup([]);
		expect((screen.getByLabelText(/Pull request title/) as HTMLInputElement).value).toBe(
			'Update db-1: Labels',
		);
		expect(screen.getByRole('button', { name: 'Open pull request' })).toBeTruthy();
		expect(screen.queryByText(/This adds to/)).toBeNull();
	});

	it('with an open PR, adds to it and does not pass the draft defaults off as its own', () => {
		setup([openPR]);
		expect(screen.getByText(/This adds to/).textContent).toContain('PR #41');
		expect((screen.getByLabelText(/Pull request title/) as HTMLInputElement).value).toBe('');
		expect(screen.queryByText(/## Changes/)).toBeNull();
		expect(screen.getByText(/PR #41 keeps its own text/)).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Add to PR #41' })).toBeTruthy();
	});

	it('a conflict is said in place, with the PR it names one click away', async () => {
		setup([openPR]);
		refuse(
			409,
			"conflict: pull request #41's branch was changed outside the console (pushed to or deleted); nothing was pushed and the draft is kept: merge or close #41, then propose\n",
		);
		await fireEvent.click(screen.getByRole('button', { name: 'Add to PR #41' }));
		const alert = await screen.findByRole('alert');
		expect(alert.textContent).toContain('Not proposed: this needs a decision first.');
		expect(alert.textContent).toContain(
			"pull request #41's branch was changed outside the console",
		);
		expect(alert.textContent).not.toContain('/api/');
		expect(alert.textContent).not.toContain('conflict:');
		expect(screen.getByRole('link', { name: 'Open PR #41' }).getAttribute('href')).toBe(
			openPR.prURL,
		);
	});

	it('an unreachable forge offers to try again', async () => {
		setup([]);
		refuse(
			503,
			'temporarily unavailable: the forge did not say whether this draft already has a pull request; nothing was pushed and the draft is kept, so propose again in a moment\n',
		);
		await fireEvent.click(screen.getByRole('button', { name: 'Open pull request' }));
		const alert = await screen.findByRole('alert');
		expect(alert.textContent).toContain('Not proposed yet.');
		await fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
		const proposes = fetchMock.mock.calls.filter(([url]) =>
			String(url).startsWith('/api/draft/propose'),
		);
		expect(proposes).toHaveLength(2);
	});

	it('a propose that decided something says so', async () => {
		const onclose = vi.fn();
		inventory.apply({ projects: [], proposals: [] } as unknown as Inventory);
		drafts.drafts = [{ project: 'team-db', draft }];
		render(ProposeSheet, { project: 'team-db', onclose });
		fetchMock.mockImplementation(async (url: string) =>
			url.startsWith('/api/draft/propose')
				? Response.json({ branch: 'b', warning: 'db-1 changed on main; pushed on its own.' })
				: new Response(null, { status: 204 }),
		);
		await fireEvent.click(screen.getByRole('button', { name: 'Open pull request' }));
		await vi.waitFor(() => expect(onclose).toHaveBeenCalled());
		expect(ui.toasts.map((t) => [t.kind, t.msg])).toContainEqual([
			'warning',
			'db-1 changed on main; pushed on its own.',
		]);
	});
});

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';
import type { DraftItem, DraftView } from '#lib/api.js';
import { drafts } from '#lib/state/drafts.svelte.js';
import ReviewItem from './ReviewItem.svelte';

// The Changes pane's propose holds to the same deletion yes as the review
// sheet: given to the set it saw, sent with the propose, dropped when the
// lane shows another set.

const item: DraftItem = {
	kind: 'edit',
	namespace: 'web-prod',
	name: 'web-1',
	changes: [{ field: 'Labels', action: 'add', to: 'tier=gold' }],
};
const oldVM = { kind: 'VirtualMachine', namespace: 'web-prod', name: 'old-vm' };
const lane = (hidden: number): DraftView => ({
	base: 'main',
	branch: 'draft',
	count: 1,
	items: [item],
	prune: [oldVM],
	pruneHidden: hidden,
});

let fetchMock: ReturnType<typeof vi.fn>;
beforeEach(() => {
	fetchMock = vi.fn(async () => new Response(JSON.stringify({ prNumber: 7 }), { status: 200 }));
	vi.stubGlobal('fetch', fetchMock);
});
afterEach(() => {
	vi.unstubAllGlobals();
	drafts.reset();
});

describe('ReviewItem', () => {
	it('holds the propose to the yes, sends what it saw, and asks again when the set moves', async () => {
		const props = { project: 'team-web', item, vm: null, onproposed: vi.fn() };
		const { rerender } = render(ReviewItem, { ...props, lane: lane(1) });
		const propose = screen.getByRole('button', {
			name: 'Propose pull request',
		}) as HTMLButtonElement;
		expect(propose.disabled).toBe(true);
		const yes = screen.getByLabelText(/Delete these 2 when this merges/) as HTMLInputElement;
		await fireEvent.click(yes);
		expect(propose.disabled).toBe(false);
		await fireEvent.click(propose);
		const [, init] = fetchMock.mock.calls.find(([url]) => String(url).includes('/draft/propose'))!;
		expect(JSON.parse(init.body)).toMatchObject({ prune: [oldVM], pruneHidden: 1 });

		// The same set keeps the yes; another drops it.
		expect(
			(screen.getByLabelText(/Delete these 2 when this merges/) as HTMLInputElement).checked,
		).toBe(true);
		await rerender({ ...props, lane: lane(2) });
		const again = screen.getByLabelText(/Delete these 3 when this merges/) as HTMLInputElement;
		expect(again.checked).toBe(false);
	});
});

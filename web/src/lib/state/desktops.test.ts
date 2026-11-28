import { describe, expect, it, vi } from 'vitest';
import type { Desktops, Inventory } from '#lib/api.js';

const pullDesktops = vi.fn<() => Promise<Desktops>>();
vi.mock('#lib/api.js', () => ({
	api: { desktops: () => pullDesktops() },
	Unauthorized: class {},
	withRetry: vi.fn(),
}));

import { desktops } from '#lib/state/desktops.svelte.js';
import { inventory } from '#lib/state/inventory.svelte.js';

const frame = (repo: string) =>
	({
		projects: [{ name: 'p', repo, namespaces: [{ namespace: 'web', vms: [] }] }],
	}) as unknown as Inventory;
const answer: Desktops = {
	synced: true,
	classes: [],
	pools: [],
	desktops: [],
	claims: [],
	unlisted: [],
};

describe('desktops.covered', () => {
	it('is what the answer was asked for, never a frame since', async () => {
		inventory.apply(frame(''));
		pullDesktops.mockResolvedValue(answer);
		await desktops.pull();
		expect([...desktops.covered]).toEqual([]);

		// The project gains a repo: the answer still covers what it was asked for.
		inventory.apply(frame('https://forge/p.git'));
		expect([...desktops.covered]).toEqual([]);
		let land!: (d: Desktops) => void;
		pullDesktops.mockReturnValue(new Promise((r) => (land = r)));
		const pulled = desktops.pull();
		expect([...desktops.covered]).toEqual([]);
		land(answer);
		await pulled;
		expect([...desktops.covered]).toEqual(['web']);
		expect(desktops.may?.lists('web', 'vdipools')).toBe(true);
	});
});

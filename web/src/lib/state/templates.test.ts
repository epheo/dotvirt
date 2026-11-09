import { describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ templates: vi.fn() }));
vi.mock('#lib/api.js', () => ({ api, Unauthorized: class extends Error {} }));

import { templates } from './templates.svelte';

describe('templates.reset', () => {
	it('drops a load that lands after sign-out, and the next load fetches again', async () => {
		let resolve!: (v: { templates: unknown[] }) => void;
		api.templates.mockReturnValueOnce(new Promise((r) => (resolve = r)));
		const late = templates.load();
		templates.reset();
		resolve({ templates: [{ name: 'stale' }] });
		await late;
		expect(templates.list).toBeNull();

		api.templates.mockResolvedValueOnce({ templates: [] });
		await templates.load();
		expect(api.templates).toHaveBeenCalledTimes(2);
		expect(templates.list).toEqual([]);
	});
});

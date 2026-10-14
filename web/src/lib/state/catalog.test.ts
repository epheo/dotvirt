import { describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ templates: vi.fn() }));
vi.mock('#lib/api.js', () => ({ api, Unauthorized: class extends Error {} }));

import { catalog } from './catalog.svelte';

describe('catalog.reset', () => {
	it('drops a load that lands after sign-out, and the next load fetches again', async () => {
		let resolve!: (v: { templates: unknown[] }) => void;
		api.templates.mockReturnValueOnce(new Promise((r) => (resolve = r)));
		const late = catalog.load();
		catalog.reset();
		resolve({ templates: [{ name: 'stale' }] });
		await late;
		expect(catalog.templates).toBeNull();

		api.templates.mockResolvedValueOnce({ templates: [] });
		await catalog.load();
		expect(api.templates).toHaveBeenCalledTimes(2);
		expect(catalog.templates).toEqual([]);
	});
});

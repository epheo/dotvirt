import { afterEach, describe, expect, it, vi } from 'vitest';

// The real withRetry, around stubbed reads: the backoff and its stop are
// what these tests hold.
const reads = vi.hoisted(() => ({ networks: vi.fn(), policies: vi.fn() }));
vi.mock('#lib/api.js', async (importOriginal) => ({
	...(await importOriginal<typeof import('#lib/api.js')>()),
	api: reads,
}));

import { inventory } from './inventory.svelte';

const NET = { networks: [], uplinks: [], physicalAdapters: [], nmstatePresent: false };

afterEach(() => {
	inventory.reset();
	vi.useRealTimers();
	reads.networks.mockReset();
	reads.policies.mockReset();
});

describe('inventory.loadNet', () => {
	it("makes a person's Retry one attempt, busy while it runs", async () => {
		reads.networks.mockRejectedValue(new Error('networks down'));
		reads.policies.mockResolvedValue({ policies: [] });
		const pull = inventory.loadNet({ retries: 0 });
		expect(inventory.netBusy).toBe(true);
		await pull;
		expect(reads.networks).toHaveBeenCalledTimes(1);
		expect(inventory.netBusy).toBe(false);
		expect(inventory.netError).toBe('networks down');
	});

	it('stops retrying when its caller goes away, and writes nothing', async () => {
		vi.useFakeTimers();
		reads.networks.mockRejectedValue(new Error('networks down'));
		reads.policies.mockResolvedValue({ policies: [] });
		const owner = new AbortController();
		const pull = inventory.loadNet({ signal: owner.signal });
		await vi.advanceTimersByTimeAsync(0);
		owner.abort();
		await vi.advanceTimersByTimeAsync(60_000);
		await pull;
		expect(reads.networks).toHaveBeenCalledTimes(1);
		expect(inventory.netError).toBe('');
		expect(inventory.netBusy).toBe(false);
	});

	it('lets only the newest pull write', async () => {
		let answer!: (v: unknown) => void;
		reads.networks.mockReturnValueOnce(new Promise((r) => (answer = r))).mockResolvedValue(NET);
		reads.policies.mockResolvedValue({ policies: [] });
		const first = inventory.loadNet();
		await inventory.loadNet({ retries: 0 });
		answer({ ...NET, nmstatePresent: true });
		await first;
		expect(inventory.nmstatePresent).toBe(false);
	});
});

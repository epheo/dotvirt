import { describe, expect, it, vi } from 'vitest';
import type { Inventory } from '$lib/api';

vi.mock('$lib/api', () => ({ api: {}, Unauthorized: class {}, withRetry: vi.fn() }));

import { inventory } from '$lib/state/inventory.svelte';

const frame = (power: string) =>
	JSON.parse(
		JSON.stringify({
			projects: [
				{
					name: 'p',
					namespaces: [
						{
							namespace: 'web',
							vms: [
								{ name: 'a', namespace: 'web', power: 'On' },
								{ name: 'b', namespace: 'web', power },
							],
						},
					],
				},
			],
		}),
	) as Inventory;

describe('inventory.apply', () => {
	it('keeps unchanged VMs identical across frames', () => {
		inventory.apply(frame('On'));
		const first = inventory.inventory;
		const [a, b] = inventory.allVMs;

		inventory.apply(frame('On'));
		expect(inventory.inventory).toBe(first);

		inventory.apply(frame('Off'));
		expect(inventory.inventory).not.toBe(first);
		expect(inventory.allVMs[0]).toBe(a);
		expect(inventory.allVMs[1]).not.toBe(b);
		expect(inventory.findVM('web', 'b')?.power).toBe('Off');
	});
});

import { describe, expect, it } from 'vitest';
import type { HostCapacity, VM } from '#lib/api.js';
import { headroom } from '#lib/headroom.js';

const GiB = 2 ** 30;

function vm(cpu: number, memory: string, name = `vm-${cpu}-${memory}`): VM {
	return {
		namespace: 'ns',
		name,
		power: 'On',
		cpuCores: cpu,
		memory,
		sourceFile: '',
		sync: 'Synced',
	};
}

const cap: HostCapacity = {
	updated: 0,
	nodes: [
		{
			node: 'w1',
			cpuAllocatable: 16,
			vcpuAllocated: 10,
			memAllocatable: 59.6 * GiB,
			memAllocated: 22.4 * GiB,
		},
		{
			node: 'w2',
			cpuAllocatable: 16,
			vcpuAllocated: 6,
			memAllocatable: 59.6 * GiB,
			memAllocated: 11.2 * GiB,
		},
		{
			node: 'w3',
			cpuAllocatable: 16,
			vcpuAllocated: 2,
			memAllocatable: 59.6 * GiB,
			memAllocated: 5.6 * GiB,
		},
	],
};

describe('headroom', () => {
	it('counts by memory per host, skipping hosts that take no new VMs', () => {
		const h = headroom(
			cap,
			[
				{ name: 'w1', ready: true },
				{ name: 'w2', ready: true },
				{ name: 'w3', ready: true, unschedulable: true, maintenance: true },
			],
			[vm(2, '4Gi', 'a'), vm(2, '4Gi', 'b'), vm(4, '8Gi', 'c')],
			null,
		)!;
		expect(h.size).toEqual({ cpu: 2, memory: '4Gi', name: undefined });
		// floor(37.2 / 4) + floor(48.4 / 4) = 9 + 12
		expect(h.count).toBe(21);
		expect(h.excluded).toEqual([{ node: 'w3', reason: 'in maintenance' }]);
		expect(h.vcpuPerCore).toBeCloseTo(0.5);
	});

	it('counts every host when nodes are unreadable', () => {
		expect(headroom(cap, null, [vm(2, '4Gi')], null)!.excluded).toEqual([]);
	});

	it('breaks a size tie toward the larger VM', () => {
		const h = headroom(cap, null, [vm(2, '4Gi'), vm(4, '8Gi')], null)!;
		expect(h.size.memory).toBe('8Gi');
	});

	it('has nothing to say without a sized VM', () => {
		expect(headroom(cap, null, [], null)).toBeNull();
	});
});

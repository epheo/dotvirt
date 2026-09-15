import { describe, expect, it } from 'vitest';
import type { Options, VM } from '$lib/api';
import { vmSizing } from '$lib/sizing';

function vm(over: Partial<VM> = {}): VM {
	return {
		namespace: 'ns-a',
		name: 'vm-a',
		power: 'On',
		sourceFile: 'vms/vm-a.yaml',
		sync: 'Synced',
		...over,
	};
}

const options = {
	instancetypes: [{ name: 'u1.medium', cpu: 2, memory: '4Gi' }],
} as Options;

describe('vmSizing', () => {
	it('prefers the manifest sizing', () => {
		const s = vmSizing(vm({ cpuCores: 8, memory: '16Gi', vcpus: 2, memoryActual: '4Gi' }), options);
		expect(s).toEqual({ cpu: 8, memory: '16Gi', from: undefined });
	});

	it('resolves an instancetype through the catalog and names it', () => {
		const s = vmSizing(vm({ instancetype: 'u1.medium' }), options);
		expect(s).toEqual({ cpu: 2, memory: '4Gi', from: 'u1.medium' });
	});

	it('falls back to the live VMI when the flavor is not in the catalog', () => {
		const live = vm({ instancetype: 'gone', vcpus: 4, memoryActual: '8Gi' });
		expect(vmSizing(live, options)).toEqual({ cpu: 4, memory: '8Gi', from: undefined });
		expect(vmSizing(live, null)).toEqual({ cpu: 4, memory: '8Gi', from: undefined });
	});

	it('leaves a stopped VM with an unknown flavor unsized', () => {
		expect(vmSizing(vm({ instancetype: 'gone', vcpus: 0 }), null)).toEqual({
			cpu: undefined,
			memory: undefined,
			from: undefined,
		});
	});
});

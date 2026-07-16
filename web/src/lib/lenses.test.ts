import { describe, expect, it } from 'vitest';
import type { Network, VM } from '$lib/api';
import {
	DEFAULT_CLASS,
	NO_NETWORK,
	NO_STORAGE,
	POD_NETWORK,
	vmNetworkKeys,
	vmStorageKeys,
} from '$lib/lenses';

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

const catalog: Network[] = [
	{
		name: 'network-a',
		kind: 'default',
		scope: 'project',
		namespace: 'ns-a',
		backing: 'UserDefinedNetwork',
	},
	{
		name: 'vlan-100',
		kind: 'vlan',
		scope: 'shared',
		attachRef: 'ns-a/vlan-100',
		backing: 'ClusterUserDefinedNetwork',
	},
];

describe('vmNetworkKeys', () => {
	it('falls back to the no-network key without NICs', () => {
		expect(vmNetworkKeys(vm())).toEqual([NO_NETWORK]);
	});

	it('labels the default NIC "Pod network" before the catalog has loaded', () => {
		expect(vmNetworkKeys(vm({ networks: [{ name: 'default', network: 'pod' }] }))).toEqual([
			POD_NETWORK,
		]);
	});

	it('resolves the default NIC to the primary port group of its namespace', () => {
		const v = vm({ networks: [{ name: 'default', network: 'pod' }] });
		expect(vmNetworkKeys(v, catalog)).toEqual(['network-a']);
	});

	it('resolves a multus ref through the catalog to the port-group name', () => {
		const v = vm({ networks: [{ name: 'net1', network: 'ns-a/vlan-100' }] });
		expect(vmNetworkKeys(v, catalog)).toEqual(['vlan-100']);
	});

	it('keeps the raw ref when the catalog has no match', () => {
		const v = vm({ networks: [{ name: 'net1', network: 'ns-b/unknown' }] });
		expect(vmNetworkKeys(v, catalog)).toEqual(['ns-b/unknown']);
	});

	it('is a stable identity: equal memberships share keys, different ones do not', () => {
		const a = vm({ networks: [{ name: 'net1', network: 'ns-a/vlan-100' }] });
		const b = vm({ name: 'vm-b', networks: [{ name: 'eth0', network: 'ns-a/vlan-100' }] });
		const c = vm({ name: 'vm-c', networks: [{ name: 'net1', network: 'ns-a/other' }] });
		expect(vmNetworkKeys(a, catalog)).toEqual(vmNetworkKeys(b, catalog));
		expect(vmNetworkKeys(a, catalog)).not.toEqual(vmNetworkKeys(c, catalog));
	});

	it('dedupes NICs on the same port group', () => {
		const v = vm({
			networks: [
				{ name: 'net1', network: 'ns-a/vlan-100' },
				{ name: 'net2', network: 'ns-a/vlan-100' },
			],
		});
		expect(vmNetworkKeys(v, catalog)).toEqual(['vlan-100']);
	});
});

describe('vmStorageKeys', () => {
	it('falls back to the no-storage key without provisioned disks', () => {
		expect(vmStorageKeys(vm())).toEqual([NO_STORAGE]);
		expect(vmStorageKeys(vm({ disks: [{ name: 'root', type: 'containerDisk' }] }))).toEqual([
			NO_STORAGE,
		]);
	});

	it('groups a classless dataVolume under the cluster default', () => {
		const v = vm({ disks: [{ name: 'root', type: 'dataVolume' }] });
		expect(vmStorageKeys(v)).toEqual([DEFAULT_CLASS]);
	});

	it('dedupes disks on the same class and keeps distinct classes apart', () => {
		const v = vm({
			disks: [
				{ name: 'root', type: 'dataVolume', storageClass: 'fast' },
				{ name: 'data', type: 'dataVolume', storageClass: 'fast' },
				{ name: 'logs', type: 'dataVolume', storageClass: 'slow' },
			],
		});
		expect(vmStorageKeys(v)).toEqual(['fast', 'slow']);
		const same = vm({
			name: 'vm-b',
			disks: [{ name: 'x', type: 'dataVolume', storageClass: 'fast' }],
		});
		expect(vmStorageKeys(same)).toContain('fast');
	});
});

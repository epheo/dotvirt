// The grouping keys behind the inventory tree's Networks/Storage lenses,
// shared with the page's scope filtering so tree groups and grid scope can
// never disagree on what belongs to a key.
import type { Network, VM } from '$lib/api';
import { resolveNIC } from '$lib/networks';

// vCenter model: the tree is a scope selector, the center pane is the VM grid.
// Every inventory level the tree can focus is one of these.
export type Scope =
	| { kind: 'all' }
	| { kind: 'project'; project: string }
	| { kind: 'namespace'; project: string; namespace: string }
	| { kind: 'node'; node: string }
	| { kind: 'network'; network: string }
	| { kind: 'storage'; storageClass: string };

export const NO_NETWORK = '(no network)';
export const POD_NETWORK = 'Pod network';
export const NO_STORAGE = '(no provisioned storage)';
export const DEFAULT_CLASS = '(cluster default)';

/**
 * The networks a VM appears under: one key per distinct port group, resolved
 * through the catalog so the lens groups by the vCenter port-group name (a
 * tenant's primary "VM Network", a shared VLAN…) rather than the raw OVN-K ref —
 * otherwise every primary-UDN VM collapses under a single "pod" key. Falls back
 * to the raw ref (then "Pod network") before the catalog has loaded.
 */
export function vmNetworkKeys(vm: VM, networks: Network[] = []): string[] {
	const keys = new Set<string>();
	for (const nic of vm.networks ?? []) {
		const pg = resolveNIC(nic, vm.namespace, networks);
		if (pg) keys.add(pg.name);
		else if (nic.network && nic.network !== 'pod') keys.add(nic.network);
		else keys.add(POD_NETWORK);
	}
	return keys.size ? [...keys] : [NO_NETWORK];
}

/**
 * The storage classes a VM appears under: one key per distinct dataVolume
 * class (the provisioned disks — container/empty/cloud-init disks have no
 * class to group by).
 */
export function vmStorageKeys(vm: VM): string[] {
	const classes = [
		...new Set(
			(vm.disks ?? [])
				.filter((d) => d.type === 'dataVolume')
				.map((d) => d.storageClass || DEFAULT_CLASS)
		)
	];
	return classes.length ? classes : [NO_STORAGE];
}

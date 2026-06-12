// The grouping keys behind the inventory tree's Networks/Storage lenses,
// shared with the page's scope filtering so tree groups and grid scope can
// never disagree on what belongs to a key.
import type { VM } from '$lib/api';

export const NO_NETWORK = '(no network)';
export const NO_STORAGE = '(no provisioned storage)';
export const DEFAULT_CLASS = '(cluster default)';

/** The networks a VM appears under: one key per distinct NIC network. */
export function vmNetworkKeys(vm: VM): string[] {
	const nets = [...new Set((vm.networks ?? []).map((n) => n.network || '(unnamed)'))];
	return nets.length ? nets : [NO_NETWORK];
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

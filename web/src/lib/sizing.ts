import type { Options, VM } from '$lib/api';

export interface Sizing {
	cpu?: number;
	memory?: string;
	// The instancetype the numbers came from, when the catalog resolved one.
	from?: string;
}

// An instancetype-sized VM carries no inline domain.cpu/memory in git, so the
// manifest fields are legitimately empty. Every read view resolves sizing the
// same way: manifest first (custom-sized), then the instancetype catalog
// (covers stopped VMs), then the live VMI (a running VM whose flavor isn't in
// the cluster catalog). KubeVirt rejects inline sizing beside an instancetype,
// so a resolved flavor is the source of both numbers.
export function vmSizing(vm: VM, options: Options | null): Sizing {
	const it = vm.instancetype
		? options?.instancetypes?.find((i) => i.name === vm.instancetype)
		: undefined;
	return {
		cpu: vm.cpuCores ?? it?.cpu ?? (vm.vcpus || undefined),
		memory: vm.memory ?? it?.memory ?? vm.memoryActual,
		from: it?.name,
	};
}

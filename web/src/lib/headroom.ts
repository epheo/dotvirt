import type { HostCapacity, HostCapacityNode, Node, Options, VM } from '#lib/api.js';
import { bytes, quantity, quantityBytes } from '#lib/format.js';
import { vmSizing } from '#lib/sizing.js';

export type Headroom = {
	// More VMs of the reference size that fit on the schedulable hosts.
	count: number;
	size: { cpu: number; memory: string; name?: string };
	// vCPU committed per allocatable core on the counted hosts.
	vcpuPerCore: number;
	// Hosts left out of the count, with why.
	excluded: { node: string; reason: string }[];
};

// How many more VMs of the fleet's most common size fit. Memory is the bound:
// guest memory is reserved per VM and never shared, while vCPU is time-shared,
// so it is reported as a commitment ratio instead of a limit. null when there
// is nothing to size against.
export function headroom(
	cap: HostCapacity,
	nodes: Node[] | null,
	vms: VM[],
	options: Options | null,
): Headroom | null {
	const size = commonSize(vms, options);
	if (!size) return null;
	const need = quantityBytes(size.memory);
	if (!(need > 0)) return null;
	const { hosts, excluded } = schedulable(cap, nodes);
	let count = 0;
	let vcpu = 0;
	let cores = 0;
	for (const c of hosts) {
		count += Math.floor(free(c) / need);
		vcpu += c.vcpuAllocated ?? 0;
		cores += c.cpuAllocatable;
	}
	return { count, size, vcpuPerCore: cores > 0 ? vcpu / cores : 0, excluded };
}

// The size most VMs share, named by its instancetype when one supplies it.
// Ties go to the larger size, so the count never flatters the fleet.
function commonSize(vms: VM[], options: Options | null): Headroom['size'] | null {
	const tally = new Map<string, { size: Headroom['size']; n: number; bytes: number }>();
	for (const vm of vms) {
		const s = vmSizing(vm, options);
		if (!s.cpu || !s.memory) continue;
		const key = `${s.cpu}|${s.memory}`;
		const t = tally.get(key);
		if (t) t.n++;
		else
			tally.set(key, {
				size: { cpu: s.cpu, memory: s.memory, name: s.from },
				n: 1,
				bytes: quantityBytes(s.memory) || 0,
			});
	}
	let best: { size: Headroom['size']; n: number; bytes: number } | null = null;
	for (const t of tally.values())
		if (!best || t.n > best.n || (t.n === best.n && t.bytes > best.bytes)) best = t;
	return best?.size ?? null;
}

export type MemoryFit = {
	// Hosts that could start the VM at the new size, out of those taking VMs.
	fits: number;
	hosts: number;
	// The host with the most free guest memory, in bytes.
	best: { node: string; free: number } | null;
};

// Where a VM could start with `need` bytes of guest memory. A hint, not a
// guard: the scheduler decides, and capacity moves before the PR merges. The
// VM's current memory goes back to its own host first, since the restart
// frees it. null when no host takes VMs.
export function memoryFit(
	cap: HostCapacity,
	nodes: Node[] | null,
	need: number,
	own: { node?: string; bytes: number },
): MemoryFit | null {
	const { hosts } = schedulable(cap, nodes);
	if (!hosts.length) return null;
	let fits = 0;
	let best: MemoryFit['best'] = null;
	for (const c of hosts) {
		const f = free(c) + (c.node === own.node ? own.bytes : 0);
		if (f >= need) fits++;
		if (!best || f > best.free) best = { node: c.node, free: f };
	}
	return { fits, hosts: hosts.length, best };
}

export type Placement = MemoryFit & {
	// Hosts that take no new VMs, with why; hosts adds these back for the
	// "47 of 48" count a new VM is placed against.
	excluded: Headroom['excluded'];
	total: number;
};

// Where a new VM needing `need` bytes of guest memory could start, counted
// against every host so the ones left out are named. A hint, as memoryFit.
export function placement(cap: HostCapacity, nodes: Node[] | null, need: number): Placement | null {
	if (!cap.nodes.length) return null;
	const f = memoryFit(cap, nodes, need, { bytes: 0 }) ?? { fits: 0, hosts: 0, best: null };
	const { excluded } = schedulable(cap, nodes);
	return { ...f, excluded, total: f.hosts + excluded.length };
}

// The capacity line for giving a VM `size` of guest memory: silent unless
// it is an increase over what the VM holds and capacity is known. subject
// names the VM in the line, for lists that hold several.
export function memoryHint(
	room: { cap: HostCapacity | null; nodes: Node[] | null } | null,
	vm: { name: string; nodeName?: string; memoryActual?: string },
	declared: string | undefined,
	size: string,
	subject = false,
): { warn: boolean; text: string } | null {
	if (!room?.cap) return null;
	const need = quantityBytes(size);
	const have = quantityBytes(vm.memoryActual || declared || '') || 0;
	if (!(need > have)) return null;
	const f = memoryFit(room.cap, room.nodes, need, { node: vm.nodeName, bytes: have });
	if (!f?.best) return null;
	const most = `Most room: ${f.best.node}, ${bytes(f.best.free)} free.`;
	const hosts = `${f.hosts} host${f.hosts === 1 ? '' : 's'}`;
	const at = quantity(size);
	return f.fits
		? {
				warn: false,
				text: `${subject ? `${vm.name} at ${at} fits` : 'Fits'} on ${f.fits} of ${hosts}. ${most}`,
			}
		: {
				warn: true,
				text: `No host has ${at} free. ${most} ${vm.name} could not start until a host frees memory.`,
			};
}

const free = (c: HostCapacityNode) => Math.max(0, c.memAllocatable - (c.memAllocated ?? 0));

// Hosts in maintenance, cordoned or not ready take no new VMs. Without node
// read rights every capacity host counts.
function schedulable(cap: HostCapacity, nodes: Node[] | null) {
	const byName = new Map((nodes ?? []).map((n) => [n.name, n]));
	const hosts: HostCapacityNode[] = [];
	const excluded: Headroom['excluded'] = [];
	for (const c of cap.nodes) {
		const n = byName.get(c.node);
		const reason = !n
			? ''
			: n.maintenance
				? 'in maintenance'
				: !n.ready
					? 'not ready'
					: n.unschedulable
						? 'cordoned'
						: '';
		if (reason) excluded.push({ node: c.node, reason });
		else hosts.push(c);
	}
	return { hosts, excluded };
}

import type { NetworkAdd, VM } from '#lib/api.js';
import { quantity } from '#lib/format.js';
import { validName } from '#lib/validate.js';

// Names for the devices the client adds to a VM. The server refuses a disk or
// an interface whose name the VM already uses, and it applies adds before
// removals, so a name being removed in the same edit still counts as taken.

// The disks a generated VM carries besides its extra disks.
export const RESERVED_DISKS = ['rootdisk', 'cloudinitdisk'];

// freeName is prefix plus the first number from 1 that no taken name uses: a
// removed disk leaves a gap the next add fills, never a repeat.
export function freeName(prefix: string, taken: Iterable<string>): string {
	const used = new Set(taken);
	for (let n = 1; ; n++) if (!used.has(`${prefix}${n}`)) return `${prefix}${n}`;
}

// diskNameError says why a typed disk name cannot be added, or ''.
export function diskNameError(name: string, others: Iterable<string>): string {
	if (!validName(name)) return `Disk ${name || 'name'}: lowercase letters, digits and "-" only.`;
	if (new Set(others).has(name)) return `${name} is already a disk on this VM.`;
	return '';
}

// interfaceName is the server's default for a network add: the ref's last
// segment (model.InterfaceName).
export const interfaceName = (ref: string) => ref.slice(ref.lastIndexOf('/') + 1);

// networkAdds attaches each ref through the server's default interface name
// when it is free, else through "<name>-2", "-3": the first not taken by the
// VM or an earlier add.
export function networkAdds(refs: string[], taken: Iterable<string>): NetworkAdd[] {
	const used = new Set(taken);
	return refs.map((name) => {
		const base = interfaceName(name);
		let iface = base;
		for (let n = 2; used.has(iface); n++) iface = `${base}-${n}`;
		used.add(iface);
		return iface === base ? { name } : { name, interface: iface };
	});
}

// lostWithVM names the disks a VM's deletion destroys: the ones its own
// DataVolume templates provisioned. A claim the VM only references stays.
export function lostWithVM(vm: Pick<VM, 'disks'>): string {
	const names = (vm.disks ?? [])
		.filter((d) => d.ownedByVM)
		.map((d) => (d.size ? `${d.name} (${quantity(d.size)})` : d.name));
	if (!names.length) return '';
	if (names.length === 1) return `Its disk ${names[0]} is deleted with it, and the data on it.`;
	const all = `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`;
	return `Its disks ${all} are deleted with it, and the data on them.`;
}

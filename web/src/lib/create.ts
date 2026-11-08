// The create sheet's defaults: each one is read off what already exists
// where the user was (names, sizes, networks, classes), so a create asks only
// for what cannot be guessed.
import type { Instancetype, Network, Options, Preference, StorageClassInfo, VM } from '#lib/api.js';
import { quantity, quantityBytes } from '#lib/format.js';
import { attachRef, resolveNIC } from '#lib/networks.js';
import { vmSizing } from '#lib/sizing.js';

export type NextName = {
	name: string;
	// The highest existing name in the series the suggestion continues.
	after?: string;
};

// The next free name in the series most names in taken follow ("web-412" ->
// "web-413"), keeping zero padding; `${fallback}-1` when nothing is numbered.
export function nextFreeName(taken: Iterable<string>, fallback: string): NextName {
	const names = new Set(taken);
	const series = new Map<string, { n: number; max: number; width: number; at: string }>();
	for (const name of names) {
		const m = /^(.*?)(\d+)$/.exec(name);
		if (!m || !m[1]) continue;
		const num = Number(m[2]);
		const s = series.get(m[1]);
		if (!s) series.set(m[1], { n: 1, max: num, width: m[2].length, at: name });
		else {
			s.n++;
			if (num > s.max) Object.assign(s, { max: num, width: m[2].length, at: name });
		}
	}
	let best: [string, { n: number; max: number; width: number; at: string }] | null = null;
	for (const e of series)
		if (!best || e[1].n > best[1].n || (e[1].n === best[1].n && e[1].max > best[1].max)) best = e;
	const [prefix, s] = best ?? [`${fallback}-`, { max: 0, width: 1, at: undefined }];
	let num = s.max + 1;
	const make = () => prefix + String(num).padStart(s.width, '0');
	while (names.has(make())) num++;
	return { name: make(), after: s.at };
}

export type Size = { it: Instancetype; count: number };

// How many of vms run at each instancetype. A custom-sized VM counts for the
// instancetype with the same vCPU and memory, so a fleet sized by hand still
// names its usual size.
export function sizeTally(vms: VM[], options: Options | null): Map<string, number> {
	const its = options?.instancetypes ?? [];
	const tally = new Map<string, number>();
	for (const vm of vms) {
		let name =
			vm.instancetype && its.some((i) => i.name === vm.instancetype) ? vm.instancetype : '';
		if (!name) {
			const s = vmSizing(vm, options);
			const mem = quantityBytes(s.memory ?? '');
			name = its.find((i) => i.cpu === s.cpu && quantityBytes(i.memory) === mem)?.name ?? '';
		}
		if (name) tally.set(name, (tally.get(name) ?? 0) + 1);
	}
	return tally;
}

// The instancetype most of vms run at; ties go to the larger size.
export function mostCommonSize(vms: VM[], options: Options | null): Size | null {
	const its = options?.instancetypes ?? [];
	let best: Size | null = null;
	for (const [name, count] of sizeTally(vms, options)) {
		const it = its.find((i) => i.name === name)!;
		if (
			!best ||
			count > best.count ||
			(count === best.count && quantityBytes(it.memory) > quantityBytes(best.it.memory))
		)
			best = { it, count };
	}
	return best;
}

// Disks made from each boot image, keyed "namespace/name" as a disk names
// its source image.
export function imageTally(vms: VM[]): Map<string, number> {
	const tally = new Map<string, number>();
	for (const vm of vms)
		for (const d of vm.disks ?? []) {
			if (d.image) tally.set(d.image, (tally.get(d.image) ?? 0) + 1);
		}
	return tally;
}

// The preference most of vms are tuned with, when one is.
export function usualPreference(vms: VM[]): string {
	const tally = new Map<string, number>();
	for (const vm of vms)
		if (vm.preference) tally.set(vm.preference, (tally.get(vm.preference) ?? 0) + 1);
	let best = '';
	for (const [p, n] of tally) if (!best || n > tally.get(best)!) best = p;
	return best;
}

// The secondary networks most of vms attach (more than half of them), among
// those still attachable.
export function usualNetworks(vms: VM[], attachable: Network[]): string[] {
	if (!vms.length) return [];
	const refs = new Set(attachable.map(attachRef));
	const tally = new Map<string, number>();
	for (const vm of vms)
		for (const ref of new Set((vm.networks ?? []).map((n) => n.network ?? '')))
			if (refs.has(ref)) tally.set(ref, (tally.get(ref) ?? 0) + 1);
	return [...tally].filter(([, n]) => n * 2 > vms.length).map(([ref]) => ref);
}

// A console password for a new VM: cloud images ship with locked ones. 16
// unambiguous alphanumerics (about 92 bits), so the bcrypt hash git keeps is
// useless to an offline attacker.
export function generatePassword(): string {
	const chars = 'abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789';
	const buf = new Uint32Array(16);
	crypto.getRandomValues(buf);
	return Array.from(buf, (n) => chars[n % chars.length]).join('');
}

// "Medium, 2 vCPU 4 GiB": the size word an instancetype name carries after
// its series ("u1.medium"), then the numbers.
export function sizeLabel(it: Instancetype): string {
	const word = it.name.slice(it.name.lastIndexOf('.') + 1);
	return `${word.charAt(0).toUpperCase()}${word.slice(1)}, ${it.cpu} vCPU ${quantity(it.memory)}`;
}

const squash = (s: string) => s.toLowerCase().replace(/[^a-z0-9]/g, '');

// The preference that tunes a guest booted from image: the longest preference
// name the image name starts with ("rhel-9.6" -> "rhel.9"), else one of the
// same OS family ("windows-2025" -> a windows.* one), else none.
export function preferenceFor(image: string, preferences: Preference[]): Preference | undefined {
	const img = squash(image);
	let best: Preference | undefined;
	for (const p of preferences) {
		const n = squash(p.name);
		if (n && img.startsWith(n) && (!best || n.length > squash(best.name).length)) best = p;
	}
	if (best) return best;
	const family = /^[a-z]+/.exec(image.toLowerCase())?.[0];
	return family
		? preferences.find((p) => /^[a-z]+/.exec(p.name.toLowerCase())?.[0] === family)
		: undefined;
}

// The first-boot login cloud images ship with, by family.
export function defaultUser(image: string): string {
	const i = image.toLowerCase();
	if (i.includes('fedora')) return 'fedora';
	if (i.includes('ubuntu')) return 'ubuntu';
	if (i.includes('debian')) return 'debian';
	return 'cloud-user';
}

// VMs attached to each network, by attach ref. A VM counts once per network
// however many NICs it puts there; the default NIC counts for its
// namespace's primary network when it has one.
export function vmCountByNetwork(vms: VM[], networks: Network[]): Map<string, number> {
	const out = new Map<string, number>();
	for (const vm of vms) {
		const seen = new Set<string>();
		for (const nic of vm.networks ?? []) {
			const n = resolveNIC(nic, vm.namespace, networks);
			if (n) seen.add(attachRef(n));
		}
		for (const ref of seen) out.set(ref, (out.get(ref) ?? 0) + 1);
	}
	return out;
}

export type Match = { start: number; end: number };

// Every case-insensitive occurrence of q in text, for highlighting.
export function matchRanges(text: string, q: string): Match[] {
	const needle = q.trim().toLowerCase();
	if (!needle) return [];
	const hay = text.toLowerCase();
	const out: Match[] = [];
	for (let i = hay.indexOf(needle); i >= 0; i = hay.indexOf(needle, i + needle.length))
		out.push({ start: i, end: i + needle.length });
	return out;
}

// Who a network serves, in words: "Project network of team-web" or
// "VLAN network, VLAN 101, team-web and team-db".
export function networkLine(n: Network, projectOf: (ns: string) => string): string {
	const owners = [
		...new Set(
			n.scope === 'shared' ? (n.namespaces ?? []).map(projectOf) : [projectOf(n.namespace ?? '')],
		),
	].filter(Boolean);
	const who =
		owners.length <= 2
			? owners.join(' and ')
			: `${owners.slice(0, 2).join(', ')} and ${owners.length - 2} more`;
	if (n.kind === 'vlan') return `VLAN network, VLAN ${n.vlan ?? '?'}${who ? `, ${who}` : ''}`;
	if (n.scope === 'shared') return `Shared network${who ? `, ${who}` : ''}`;
	return `Project network${who ? ` of ${who}` : ''}`;
}

export type NetworkHit = { net: Network; vms: number; own: boolean };

// The networks matching q (name, owners, kind or subnet), the opener's own
// project first, then by how many VMs use them, then by name.
export function searchNetworks(
	networks: Network[],
	q: string,
	opts: { project: string; projectOf: (ns: string) => string; counts: Map<string, number> },
): NetworkHit[] {
	const needle = q.trim().toLowerCase();
	const hits: NetworkHit[] = [];
	for (const net of networks) {
		const line = networkLine(net, opts.projectOf);
		const text = `${net.name} ${line} ${(net.subnets ?? []).join(' ')}`.toLowerCase();
		if (needle && !text.includes(needle)) continue;
		const own =
			net.scope === 'shared'
				? (net.namespaces ?? []).some((ns) => opts.projectOf(ns) === opts.project)
				: opts.projectOf(net.namespace ?? '') === opts.project;
		hits.push({ net, vms: opts.counts.get(attachRef(net)) ?? 0, own });
	}
	return hits.sort(
		(a, b) =>
			Number(b.own) - Number(a.own) || b.vms - a.vms || a.net.name.localeCompare(b.net.name),
	);
}

// Whether disks on a class follow their VM across hosts.
export const liveMigrates = (c: StorageClassInfo | undefined) => !!c?.profile?.shared;

// The class to offer when the chosen one pins disks to a host: a shared one,
// the default first, then the one with the most free space reported.
export function sharedClass(classes: StorageClassInfo[]): StorageClassInfo | undefined {
	return classes
		.filter(liveMigrates)
		.sort(
			(a, b) =>
				Number(!!b.default) - Number(!!a.default) ||
				(b.free ?? -1) - (a.free ?? -1) ||
				a.name.localeCompare(b.name),
		)[0];
}

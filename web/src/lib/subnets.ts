// Subnet arithmetic for the create forms: which visible networks a range
// collides with, and the nearest ranges that collide with none. "Free" means
// free among the networks the caller can see; a tenant never sees another
// tenant's ranges, and the cluster's own pod and service ranges are not in
// the network catalog.

export type CIDR = { v6: boolean; base: bigint; prefix: number };

const width = (c: { v6: boolean }) => (c.v6 ? 128 : 32);
const size = (c: CIDR) => 1n << BigInt(width(c) - c.prefix);
const last = (c: CIDR) => c.base + size(c) - 1n;

function parseV4(s: string): bigint | null {
	const parts = s.split('.');
	if (parts.length !== 4) return null;
	let v = 0n;
	for (const p of parts) {
		if (!/^\d{1,3}$/.test(p) || Number(p) > 255) return null;
		v = (v << 8n) | BigInt(p);
	}
	return v;
}

function parseV6(s: string): bigint | null {
	const halves = s.split('::');
	if (halves.length > 2) return null;
	const groups = (h: string) => (h === '' ? [] : h.split(':'));
	const head = groups(halves[0]);
	const tail = halves.length === 2 ? groups(halves[1]) : [];
	const fill = 8 - head.length - tail.length;
	if (halves.length === 1 ? fill !== 0 : fill < 1) return null;
	let v = 0n;
	for (const g of [...head, ...Array<string>(halves.length === 2 ? fill : 0).fill('0'), ...tail]) {
		if (!/^[0-9a-fA-F]{1,4}$/.test(g)) return null;
		v = (v << 16n) | BigInt(parseInt(g, 16));
	}
	return v;
}

// The network a CIDR names, host bits cleared ("10.200.1.0/16" is
// 10.200.0.0/16); null when it does not parse.
export function parseCIDR(s: string): CIDR | null {
	const m = /^([^/\s]+)\/(\d{1,3})$/.exec(s.trim());
	if (!m) return null;
	const v6 = m[1].includes(':');
	const addr = v6 ? parseV6(m[1]) : parseV4(m[1]);
	const prefix = Number(m[2]);
	if (addr === null || prefix > width({ v6 })) return null;
	const span = 1n << BigInt(width({ v6 }) - prefix);
	return { v6, prefix, base: (addr / span) * span };
}

export function formatCIDR(c: CIDR): string {
	if (!c.v6) {
		const o = [24n, 16n, 8n, 0n].map((s) => String((c.base >> s) & 255n));
		return `${o.join('.')}/${c.prefix}`;
	}
	const g = Array.from({ length: 8 }, (_, i) => (c.base >> BigInt(112 - 16 * i)) & 0xffffn);
	// The longest run of zero groups (two or more) collapses to "::".
	let best = { at: -1, len: 1 };
	for (let i = 0; i < 8;) {
		let j = i;
		while (j < 8 && g[j] === 0n) j++;
		if (j - i > best.len) best = { at: i, len: j - i };
		i = j === i ? i + 1 : j;
	}
	const hex = (xs: bigint[]) => xs.map((x) => x.toString(16)).join(':');
	const addr =
		best.at < 0 ? hex(g) : `${hex(g.slice(0, best.at))}::${hex(g.slice(best.at + best.len))}`;
	return `${addr}/${c.prefix}`;
}

export function cidrOverlaps(a: CIDR, b: CIDR): boolean {
	return a.v6 === b.v6 && a.base <= last(b) && b.base <= last(a);
}

// The networks holding a range that overlaps cidr, in catalog order.
export function overlapping<T extends { subnets?: string[] }>(cidr: string, networks: T[]): T[] {
	const c = parseCIDR(cidr);
	if (!c) return [];
	return networks.filter((n) =>
		(n.subnets ?? []).some((s) => {
			const o = parseCIDR(s);
			return !!o && cidrOverlaps(c, o);
		}),
	);
}

// Address blocks a range stays inside while searching for a free neighbor, so
// a private range is never answered with a public one.
const PRIVATE = ['10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16', '100.64.0.0/10', 'fc00::/7'].map(
	(s) => parseCIDR(s)!,
);

// How far either way the search walks before giving up: past this many
// same-size blocks the answer is no longer "nearby".
const MAX_STEPS = 4096;

// The n free ranges of cidr's size nearest to it (the block above first,
// then below, widening), never overlapping a taken range and staying inside
// the private block cidr sits in. cidr itself is offered first when free.
export function nearestFree(cidr: string, taken: string[], n = 1): string[] {
	const c = parseCIDR(cidr);
	if (!c) return [];
	const used = taken.map(parseCIDR).filter((t): t is CIDR => !!t && t.v6 === c.v6);
	const outer = PRIVATE.find((p) => p.v6 === c.v6 && p.prefix <= c.prefix && cidrOverlaps(p, c));
	const lo = outer?.base ?? 0n;
	const hi = outer ? last(outer) : (1n << BigInt(width(c))) - 1n;
	const step = size(c);
	const free = (base: bigint) =>
		base >= lo && base + step - 1n <= hi && !used.some((u) => cidrOverlaps(u, { ...c, base }));
	const out: string[] = [];
	if (free(c.base)) out.push(formatCIDR(c));
	for (let k = 1n; out.length < n && k <= BigInt(MAX_STEPS); k++) {
		for (const base of [c.base + k * step, c.base - k * step]) {
			if (out.length < n && free(base)) out.push(formatCIDR({ ...c, base }));
		}
	}
	return out;
}

export type SubnetSuggestion = { cidr: string; reason: string };

// Where a range searched from when nothing nearby says otherwise.
const FIRST_RANGE = '10.20.0.0/24';

// A free subnet for a new network, with the reason shown beside it. Next to
// the network the form was opened from, at its size, when it has a range;
// otherwise the first open range from a fixed start.
export function suggestSubnet(
	like: { name: string; subnets?: string[] } | undefined,
	taken: string[],
): SubnetSuggestion | null {
	const from = like?.subnets?.find((s) => parseCIDR(s));
	if (from && like) {
		const [next] = nearestFree(from, [...taken, from]);
		if (next) return { cidr: next, reason: `free, next to ${like.name}, same size` };
	}
	const [first] = nearestFree(FIRST_RANGE, taken);
	return first ? { cidr: first, reason: 'free, first open range' } : null;
}

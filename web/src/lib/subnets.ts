// CIDR arithmetic for the networking views: how many addresses a subnet
// holds and whether two subnets share any. IPv4 and IPv6, as bigint ranges.

interface Range {
	v6: boolean;
	start: bigint;
	end: bigint;
	prefix: number;
}

function v4(s: string): bigint | null {
	const parts = s.split('.');
	if (parts.length !== 4) return null;
	let n = 0n;
	for (const p of parts) {
		if (!/^\d{1,3}$/.test(p) || Number(p) > 255) return null;
		n = (n << 8n) | BigInt(p);
	}
	return n;
}

function v6(s: string): bigint | null {
	const halves = s.split('::');
	if (halves.length > 2) return null;
	const head = halves[0] ? halves[0].split(':') : [];
	const tail = halves.length === 2 && halves[1] ? halves[1].split(':') : [];
	const fill = halves.length === 2 ? 8 - head.length - tail.length : 0;
	if (fill < 0 || (halves.length === 1 && head.length !== 8)) return null;
	let n = 0n;
	for (const g of [...head, ...Array(fill).fill('0'), ...tail]) {
		if (!/^[0-9a-f]{1,4}$/i.test(g)) return null;
		n = (n << 16n) | BigInt(parseInt(g, 16));
	}
	return n;
}

/** The address range a CIDR covers; null when it does not parse. */
export function parseCIDR(cidr: string): Range | null {
	const [addr, len, extra] = cidr.trim().split('/');
	if (extra !== undefined || !addr || !len || !/^\d+$/.test(len)) return null;
	const isV6 = addr.includes(':');
	const bits = isV6 ? 128 : 32;
	const prefix = Number(len);
	const base = isV6 ? v6(addr) : v4(addr);
	if (base === null || prefix > bits) return null;
	const size = 1n << BigInt(bits - prefix);
	const start = (base / size) * size;
	return { v6: isV6, start, end: start + size - 1n, prefix };
}

/**
 * Addresses a subnet can hand out: IPv4 keeps its network and broadcast
 * addresses back (none for /31 and /32), IPv6 hands out all of them.
 */
export function usableAddresses(cidr: string): number {
	const r = parseCIDR(cidr);
	if (!r) return 0;
	const size = Number(r.end - r.start + 1n);
	return !r.v6 && r.prefix < 31 ? size - 2 : size;
}

/** Whether two subnets share any address (never across address families). */
export function overlaps(a: string, b: string): boolean {
	const x = parseCIDR(a);
	const y = parseCIDR(b);
	return !!x && !!y && x.v6 === y.v6 && x.start <= y.end && y.start <= x.end;
}

/** Whether an address falls inside a subnet. */
export function contains(cidr: string, ip: string): boolean {
	const r = parseCIDR(cidr);
	const a = parseCIDR(`${ip}/${ip.includes(':') ? 128 : 32}`);
	return !!r && !!a && r.v6 === a.v6 && r.start <= a.start && a.start <= r.end;
}

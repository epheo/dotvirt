import { describe, expect, it } from 'vitest';
import { contains, overlaps, parseCIDR, usableAddresses } from './subnets';

describe('parseCIDR', () => {
	it('normalizes host bits to the network start', () => {
		const r = parseCIDR('10.40.7.9/16')!;
		expect(r.start).toBe(BigInt(10 * 2 ** 24 + 40 * 2 ** 16));
		expect(r.end - r.start).toBe(65535n);
	});
	it('reads compressed IPv6', () => {
		const r = parseCIDR('fd00::/64')!;
		expect(r.v6).toBe(true);
		expect(r.end - r.start).toBe((1n << 64n) - 1n);
	});
	it('rejects what is not a CIDR', () => {
		for (const bad of [
			'10.0.0.0',
			'10.0.0.256/8',
			'10.0.0.0/33',
			'fd00::/129',
			'x/8',
			'1:2::3::4/64',
		])
			expect(parseCIDR(bad)).toBeNull();
	});
});

describe('usableAddresses', () => {
	it('keeps network and broadcast back on IPv4', () => {
		expect(usableAddresses('10.40.0.0/16')).toBe(65534);
		expect(usableAddresses('192.168.10.0/24')).toBe(254);
		expect(usableAddresses('10.0.0.0/31')).toBe(2);
		expect(usableAddresses('10.0.0.1/32')).toBe(1);
	});
	it('is zero for garbage', () => {
		expect(usableAddresses('nope')).toBe(0);
	});
});

describe('overlaps', () => {
	it('finds identical and nested ranges', () => {
		expect(overlaps('10.200.0.0/16', '10.200.0.0/16')).toBe(true);
		expect(overlaps('10.200.0.0/16', '10.200.5.0/24')).toBe(true);
		expect(overlaps('fd00::/48', 'fd00:0:0:1::/64')).toBe(true);
	});
	it('separates neighbours and families', () => {
		expect(overlaps('10.222.1.0/24', '10.222.2.0/24')).toBe(false);
		expect(overlaps('10.0.0.0/8', 'fd00::/8')).toBe(false);
	});
});

describe('contains', () => {
	it('places an address in its subnet', () => {
		expect(contains('10.40.0.0/16', '10.40.3.7')).toBe(true);
		expect(contains('10.40.0.0/16', '10.41.0.1')).toBe(false);
		expect(contains('10.40.0.0/16', '10.40')).toBe(false);
	});
});

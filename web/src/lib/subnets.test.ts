import { describe, expect, it } from 'vitest';
import type { Network } from '#lib/api.js';
import {
	canMeet,
	cidrOverlaps,
	contains,
	formatCIDR,
	nearestFree,
	overlapping,
	overlaps,
	parseCIDR,
	suggestSubnet,
	usableAddresses,
} from '#lib/subnets.js';

const c = (s: string) => parseCIDR(s)!;

describe('parseCIDR', () => {
	it('clears host bits and round-trips', () => {
		expect(formatCIDR(c('10.200.1.7/16'))).toBe('10.200.0.0/16');
		expect(formatCIDR(c('192.168.10.0/24'))).toBe('192.168.10.0/24');
		expect(formatCIDR(c('fd00:10:0:0::1/64'))).toBe('fd00:10::/64');
		expect(formatCIDR(c('2001:db8::/32'))).toBe('2001:db8::/32');
	});
	it('refuses what is not a CIDR', () => {
		for (const s of ['10.0.0.0', '10.0.0.256/24', '10.0.0.0/33', '1::2::3/64', 'web-net/16', ''])
			expect(parseCIDR(s), s).toBeNull();
	});
});

describe('cidrOverlaps', () => {
	it('is true for containment and equality, false for neighbors and families', () => {
		expect(cidrOverlaps(c('10.200.0.0/16'), c('10.200.5.0/24'))).toBe(true);
		expect(cidrOverlaps(c('10.200.0.0/16'), c('10.200.0.0/16'))).toBe(true);
		expect(cidrOverlaps(c('10.200.0.0/16'), c('10.201.0.0/16'))).toBe(false);
		expect(cidrOverlaps(c('10.0.0.0/8'), c('fd00::/8'))).toBe(false);
	});
});

const nets = [
	{ name: 'web-net', subnets: ['10.40.0.0/16'] },
	{ name: 'udn-frontend', subnets: ['10.200.0.0/16'] },
	{ name: 'udn-tcp-frontend', subnets: ['10.200.0.0/16'] },
	{ name: 'internal-l2', subnets: ['10.20.9.0/24'] },
	{ name: 'lan-101' },
];
const taken = nets.flatMap((n) => n.subnets ?? []);

describe('overlapping', () => {
	it('names every network already on the range', () => {
		expect(overlapping('10.200.0.0/16', nets).map((n) => n.name)).toEqual([
			'udn-frontend',
			'udn-tcp-frontend',
		]);
		expect(overlapping('10.200.128.0/17', nets).map((n) => n.name)).toHaveLength(2);
		expect(overlapping('10.41.0.0/16', nets)).toEqual([]);
		expect(overlapping('nonsense', nets)).toEqual([]);
	});
});

describe('nearestFree', () => {
	it('offers the block above first, then below', () => {
		expect(nearestFree('10.200.0.0/16', taken, 2)).toEqual(['10.201.0.0/16', '10.199.0.0/16']);
	});
	it('offers the range itself when it is free', () => {
		expect(nearestFree('10.41.0.0/16', taken)).toEqual(['10.41.0.0/16']);
	});
	it('steps over every taken neighbor', () => {
		const busy = ['10.201.0.0/16', '10.199.0.0/16', '10.202.0.0/16', '10.200.0.0/16'];
		expect(nearestFree('10.200.0.0/16', busy, 2)).toEqual(['10.198.0.0/16', '10.203.0.0/16']);
	});
	it('stays inside the private block it starts in', () => {
		expect(nearestFree('10.255.0.0/16', ['10.255.0.0/16'])).toEqual(['10.254.0.0/16']);
		expect(nearestFree('192.168.255.0/24', ['192.168.255.0/24'])).toEqual(['192.168.254.0/24']);
	});
	it('answers nothing when the block is full', () => {
		expect(nearestFree('10.0.0.0/8', ['10.0.0.0/8'])).toEqual([]);
	});
	it('works on v6', () => {
		expect(nearestFree('fd00:10::/64', ['fd00:10::/64'])).toEqual(['fd00:10:0:1::/64']);
	});
});

describe('suggestSubnet', () => {
	it('sits next to the network the form came from, at its size', () => {
		expect(suggestSubnet(nets[0], taken)).toEqual({
			cidr: '10.41.0.0/16',
			reason: 'free, next to web-net, same size',
		});
	});
	it('falls back to the first open range', () => {
		expect(suggestSubnet(undefined, ['10.20.0.0/24'])).toEqual({
			cidr: '10.20.1.0/24',
			reason: 'free, first open range',
		});
		expect(suggestSubnet({ name: 'lan-101' }, [])?.cidr).toBe('10.20.0.0/24');
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

describe('canMeet', () => {
	const net = (over: Partial<Network>): Network => ({
		name: 'n',
		kind: 'internal',
		scope: 'project',
		backing: 'UserDefinedNetwork',
		...over,
	});
	const a = net({ namespace: 'default' });
	it('meets in one namespace', () => {
		expect(canMeet(a, net({ namespace: 'default' }))).toBe(true);
	});
	it('keeps isolated namespaces apart unless a VM sits on both', () => {
		const b = net({ namespace: 'other' });
		expect(canMeet(a, b)).toBe(false);
		expect(canMeet(a, b, true)).toBe(true);
	});
	it('meets where a shared or VLAN network is published', () => {
		const shared = net({ scope: 'shared', namespaces: ['x', 'default'] });
		const vlan = net({ kind: 'vlan', scope: 'shared', namespaces: ['y'] });
		expect(canMeet(a, shared)).toBe(true);
		expect(canMeet(a, vlan)).toBe(false);
		expect(canMeet(shared, net({ scope: 'shared', namespaces: ['x'] }))).toBe(true);
	});
});

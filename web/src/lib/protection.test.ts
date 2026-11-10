import { describe, expect, it } from 'vitest';
import type { ReplicatedVolume, ReplicationPool, StorageClassInfo, VM } from '#lib/api.js';
import {
	crossTierFit,
	localFit,
	pinnedHost,
	protectionChoices,
	roomText,
	tierRooms,
} from './replication';

const GIB = 1024 ** 3;
const cls = (name: string, copies?: number, tier = 'nvme', extra: Partial<StorageClassInfo> = {}) =>
	({
		name,
		provisioner: copies === undefined ? 'other.csi' : 'r-drbd.io',
		...(copies === undefined ? {} : { replication: { copies, tier } }),
		...extra,
	}) as StorageClassInfo;
const pool = (node: string, tier: string, placeable: number) =>
	({
		name: `${tier}-${node}`,
		node,
		tier,
		totalBytes: 1000 * GIB,
		freeBytes: placeable * GIB,
		placeableBytes: placeable * GIB,
		available: true,
	}) as ReplicationPool;

const rooms = tierRooms([
	pool('h1', 'nvme', 900),
	pool('h2', 'nvme', 500),
	pool('h3', 'nvme', 1400),
	pool('h1', 'hdd', 7000),
]);

describe('protectionChoices', () => {
	it('offers one card per copy count, each saying what it gets the disk', () => {
		const [one, two, three] = protectionChoices([], null);
		expect(one).toMatchObject({ copies: 1, title: 'One copy' });
		expect(one.lines).toContain('Lost with its host.');
		expect(one.lines).toContain('Its VM stays on that host: no live migration.');
		expect(two.lines).toContain('Survives losing any one host.');
		expect(two.lines).toContain('A third host keeps a tiebreaker, with no data.');
		// Majority quorum: three copies still survive one host, not two.
		expect(three.lines).toContain('Survives losing any one host.');
		expect(three.lines.join(' ')).not.toMatch(/two hosts/);
	});

	it('lands each card on the classes asking that many copies, others left out', () => {
		const [one, two, three] = protectionChoices(
			[cls('local', 1), cls('rep', 2), cls('bulk', 2, 'hdd'), cls('plain')],
			null,
		);
		expect(one.classes.map((c) => c.info.name)).toEqual(['local']);
		expect(two.classes.map((c) => c.info.name)).toEqual(['bulk', 'rep']);
		// No class asks three copies: the card stays, with nothing to pick.
		expect(three.classes).toEqual([]);
	});

	it('puts the default class first, then the roomiest', () => {
		const [, two] = protectionChoices(
			[cls('a-nvme', 2), cls('b-hdd', 2, 'hdd'), cls('z-def', 2, 'nvme', { default: true })],
			rooms,
		);
		expect(two.classes.map((c) => c.info.name)).toEqual(['z-def', 'a-nvme', 'b-hdd']);
	});

	it('gives each class the room of its Nth-roomiest host', () => {
		const [one, two, three] = protectionChoices(
			[cls('l', 1), cls('r', 2), cls('t', 3), cls('h', 2, 'hdd')],
			rooms,
		);
		expect(one.classes[0].room).toBe(1400 * GIB);
		expect(two.classes.find((c) => c.info.name === 'r')?.room).toBe(900 * GIB);
		expect(three.classes[0].room).toBe(500 * GIB);
		// One hdd host: no room for a second copy.
		expect(two.classes.find((c) => c.info.name === 'h')?.room).toBe(0);
	});

	it('names no room where the caller may not read the pools', () => {
		const [, two] = protectionChoices([cls('r', 2)], null);
		expect(two.classes[0].room).toBeUndefined();
		expect(roomText(two.classes[0], 2, 10 * GIB)).toBeNull();
	});
});

describe('roomText', () => {
	const [, two] = protectionChoices([cls('r', 2), cls('h', 2, 'hdd')], rooms);
	const r = two.classes.find((c) => c.info.name === 'r')!;
	const h = two.classes.find((c) => c.info.name === 'h')!;

	it('says whether the disk fits at that many copies', () => {
		expect(roomText(r, 2, 100 * GIB)).toEqual({
			ok: true,
			text: 'Fits. Room on nvme: 900 GiB at 2 copies.',
		});
		expect(roomText(r, 2, 1000 * GIB)).toEqual({
			ok: false,
			text: 'Too big. Room on nvme: 900 GiB at 2 copies.',
		});
	});

	it('says only the room while the size cannot be read', () => {
		expect(roomText(r, 2, NaN)?.text).toBe('Room on nvme: 900 GiB at 2 copies.');
	});

	it('warns when no host set has room', () => {
		expect(roomText(h, 2, GIB)).toEqual({
			ok: false,
			text: 'No room on hdd for a new disk at 2 copies today.',
		});
	});
});

// Two hosts for one-copy disks: h1 has one big pool, h2 two smaller ones.
const local = tierRooms([
	pool('h1', 'nvme', 300),
	{ ...pool('h2', 'nvme', 200), name: 'nvme-h2-a' },
	{ ...pool('h2', 'nvme', 150), name: 'nvme-h2-b' },
	{ ...pool('h3', 'nvme', 900), available: false },
])[0];

describe('tierRooms hosts', () => {
	it('keeps each host its largest pool and all its pools together', () => {
		expect(local.hosts).toEqual([
			{ node: 'h1', largest: 300 * GIB, total: 300 * GIB },
			{ node: 'h2', largest: 200 * GIB, total: 350 * GIB },
		]);
	});
});

describe('localFit', () => {
	it('fits a host only when its largest pool holds every one-copy disk', () => {
		expect(localFit(local.hosts, [100 * GIB, 100 * GIB], 'h2')).toBe('fits');
		// Two pools might take them, depending on which disk lands first.
		expect(localFit(local.hosts, [150 * GIB, 150 * GIB], 'h2')).toBe('maybe');
		expect(localFit(local.hosts, [200 * GIB, 200 * GIB], 'h2')).toBe('too-big');
		expect(localFit(local.hosts, [250 * GIB], 'h2')).toBe('too-big');
	});

	it('refuses a host without a pool of the tier', () => {
		expect(localFit(local.hosts, [GIB], 'h9')).toBe('too-big');
	});

	it('holds unpinned only when every host the scheduler may pick holds them', () => {
		expect(localFit(local.hosts, [100 * GIB, 100 * GIB])).toBe('fits');
		// h2 takes each alone, so the scheduler may pick it, and its pools may not take both.
		expect(localFit(local.hosts, [150 * GIB, 150 * GIB])).toBe('maybe');
		// Only h1 takes the larger one alone, and it cannot take both.
		expect(localFit(local.hosts, [250 * GIB, 100 * GIB])).toBe('too-big');
	});
});

describe('crossTierFit', () => {
	const nvme = { hosts: local.hosts, disks: [100 * GIB] };
	const hdd = (h2: [number, number], ...disks: number[]) => ({
		disks: disks.map((d) => d * GIB),
		hosts: [
			{ node: 'h1', largest: 500 * GIB, total: 500 * GIB },
			{ node: 'h2', largest: h2[0] * GIB, total: h2[1] * GIB },
		],
	});

	it("weighs only the hosts the scheduler may pick: room for each tier's biggest", () => {
		// h2 cannot take the hdd disk alone, so the scheduler never picks it.
		expect(crossTierFit([nvme, hdd([50, 50], 100)])).toBe('fits');
	});

	it('fits only when every candidate holds every tier', () => {
		expect(crossTierFit([nvme, hdd([500, 500], 100)])).toBe('fits');
		expect(crossTierFit([nvme, hdd([100, 400], 100, 100)])).toBe('maybe');
	});

	it('is too big without a candidate, or when none holds them together', () => {
		expect(crossTierFit([nvme, { disks: [GIB], hosts: [] }])).toBe('too-big');
		expect(
			crossTierFit([{ hosts: local.hosts, disks: [200 * GIB, 200 * GIB] }, hdd([500, 500], 1)]),
		).toBe('too-big');
	});
});

describe('one-copy roomText', () => {
	const [one, two] = protectionChoices([cls('l', 1), cls('r', 2)], [local]);
	const l = one.classes[0];

	it('keeps the single-disk verdict when the VM has no other one-copy disk', () => {
		expect(roomText(l, 1, 100 * GIB, { others: [] })).toEqual({
			ok: true,
			text: 'Fits. Room on nvme: 300 GiB at one copy.',
		});
	});

	it("weighs the VM's other one-copy disks, as they share its host", () => {
		expect(roomText(l, 1, 100 * GIB, { others: [100 * GIB] })).toEqual({
			ok: true,
			text: "Fits with the VM's other one-copy disks, which share its host.",
		});
		expect(roomText(l, 1, 150 * GIB, { others: [150 * GIB] })).toEqual({
			ok: false,
			text: "May not fit on one host with the VM's other one-copy disks.",
		});
		expect(roomText(l, 1, 250 * GIB, { others: [100 * GIB] })).toEqual({
			ok: false,
			text: "Too big. No host has room on nvme for the VM's one-copy disks together.",
		});
	});

	// hdd room by host, in GiB: [largest, total].
	const hdd = (rooms: Record<string, [number, number]>, ...disks: number[]) => ({
		tier: 'hdd',
		disks: disks.map((d) => d * GIB),
		hosts: Object.entries(rooms).map(([node, [largest, total]]) => ({
			node,
			largest: largest * GIB,
			total: total * GIB,
		})),
	});

	it('fits across tiers when every host the scheduler may pick has room on each', () => {
		const tiers = [hdd({ h1: [500, 500], h2: [500, 500] }, 100)];
		expect(roomText(l, 1, 100 * GIB, { others: [], tiers })).toEqual({
			ok: true,
			text: "Fits with the VM's other one-copy disks, which share its host.",
		});
	});

	it('may not fit across tiers when a host the scheduler may pick might not hold them', () => {
		// h2 takes each hdd disk alone, so it is a candidate, and its pools may not take both.
		const tiers = [hdd({ h1: [500, 500], h2: [100, 400] }, 100, 100)];
		expect(roomText(l, 1, 100 * GIB, { others: [], tiers })).toEqual({
			ok: false,
			text: "May not fit on one host with the VM's other one-copy disks.",
		});
	});

	it('is too big across tiers when no host has room on every tier', () => {
		const tiers = [hdd({ h3: [500, 500] }, 100)];
		expect(roomText(l, 1, 100 * GIB, { others: [], tiers })).toEqual({
			ok: false,
			text: "Too big. No host has room on nvme and hdd for the VM's one-copy disks together.",
		});
	});

	it('judges every tier on the host a one-copy disk already pins', () => {
		const tiers = [hdd({}, 1)];
		expect(roomText(l, 1, 100 * GIB, { others: [], tiers, host: 'h1' })?.text).toBe(
			"Fits on h1, where the VM's one-copy disk lives. Room on nvme there: 300 GiB.",
		);
	});

	it("judges a VM that already holds a one-copy disk on that disk's host", () => {
		expect(roomText(l, 1, 250 * GIB, { others: [], host: 'h1' })).toEqual({
			ok: true,
			text: "Fits on h1, where the VM's one-copy disk lives. Room on nvme there: 300 GiB.",
		});
		// The roomiest host would take it; the VM's own host cannot.
		expect(roomText(l, 1, 250 * GIB, { others: [], host: 'h2' })).toEqual({
			ok: false,
			text: "Too big for h2, where the VM's one-copy disk lives. Room on nvme there: 200 GiB.",
		});
		expect(roomText(l, 1, 150 * GIB, { others: [150 * GIB], host: 'h2' })).toEqual({
			ok: false,
			text: 'May not fit on h2 with the other new one-copy disks. Room on nvme there: 200 GiB.',
		});
		expect(roomText(l, 1, GIB, { others: [], host: 'h3' })).toEqual({
			ok: false,
			text: "No room on nvme on h3, where the VM's one-copy disk lives.",
		});
	});

	it('leaves disks with more copies to the Nth-roomiest host', () => {
		expect(roomText(two.classes[0], 2, 100 * GIB, { others: [GIB], host: 'h2' })?.text).toBe(
			'Fits. Room on nvme: 200 GiB at 2 copies.',
		);
	});
});

describe('pinnedHost', () => {
	const vol = (claim: string, nodes: string[]) =>
		({
			name: `pvc-${claim}`,
			namespace: 'web',
			claim,
			copies: nodes.length,
			tier: 'nvme',
			sizeBytes: GIB,
			replicas: nodes.map((node) => ({ node, role: 'diskful', state: 'in-sync' })),
		}) as ReplicatedVolume;
	const vm = (...claims: string[]) =>
		({
			name: 'db',
			namespace: 'web',
			disks: claims.map((claim) => ({ name: claim, claim })),
		}) as VM;

	it("names the host of the VM's one-copy disk", () => {
		const volumes = [vol('root', ['h1', 'h2']), vol('scratch', ['h3'])];
		expect(pinnedHost(vm('root', 'scratch'), volumes)).toBe('h3');
	});

	it('names none when every disk keeps more copies', () => {
		expect(pinnedHost(vm('root'), [vol('root', ['h1', 'h2'])])).toBeUndefined();
	});
});

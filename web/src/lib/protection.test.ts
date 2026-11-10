import { describe, expect, it } from 'vitest';
import type { ReplicationPool, StorageClassInfo } from '#lib/api.js';
import { protectionChoices, roomText, tierRooms } from './replication';

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

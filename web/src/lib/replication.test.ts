import { describe, expect, it } from 'vitest';
import type { DiskProtection, ReplicatedVolume, ReplicationPool, VolumeReplica } from '#lib/api.js';
import type { DiskRow } from '#lib/storage.js';
import {
	cellFor,
	diskIfDown,
	diskNow,
	hostsOf,
	mapRows,
	protectionOf,
	rehearse,
	rowIfDown,
	standingOf,
	tierRooms,
	type MapRow,
} from './replication';

const copy = (node: string, state = 'in-sync'): VolumeReplica => ({ node, role: 'diskful', state });
const witness = (node: string, state = 'standby'): VolumeReplica => ({
	node,
	role: 'tiebreaker',
	state,
});

const vol = (name: string, replicas: VolumeReplica[], extra: Partial<ReplicatedVolume> = {}) =>
	({
		name,
		phase: 'Ready',
		sizeBytes: 1024 ** 3,
		copies: replicas.filter((r) => r.role === 'diskful').length,
		tier: 'nvme',
		replicas,
		...extra,
	}) as ReplicatedVolume;

const row = (name: string, runsOn: string, ...volumes: ReplicatedVolume[]): MapRow => ({
	key: name,
	name,
	project: 'p',
	runsOn,
	disks: volumes.map((volume) => ({ label: 'disk', volume })),
});

describe('losing a host', () => {
	it('takes a one-copy disk with it', () => {
		expect(diskIfDown(vol('a', [copy('h1')]), 'h1').fate).toBe('off');
		expect(diskIfDown(vol('a', [copy('h1')]), 'h2').fate).toBe('ok');
	});

	it('takes a disk whose only in-sync copy is there, even with a second copy catching up', () => {
		const v = vol('a', [copy('h1'), copy('h2', 'catching-up'), witness('h3')]);
		expect(diskIfDown(v, 'h1')).toEqual({
			fate: 'off',
			text: 'Its only in-sync copy is on h1',
		});
	});

	it('leaves a two-copy disk running on one copy', () => {
		expect(diskIfDown(vol('a', [copy('h1'), copy('h2'), witness('h3')]), 'h1').fate).toBe('weak');
	});

	it('still protects a three-copy disk', () => {
		const v = vol('a', [copy('h1'), copy('h2'), copy('h3')]);
		expect(diskIfDown(v, 'h1')).toEqual({ fate: 'ok', text: '2 copies left, still protected' });
	});

	it('loses a majority when the witness is already gone', () => {
		const v = vol('a', [copy('h1'), copy('h2'), witness('h3', 'unreachable')]);
		expect(diskIfDown(v, 'h1')).toEqual({ fate: 'off', text: 'Loses its majority with h1' });
	});

	it('survives losing only the witness', () => {
		const v = vol('a', [copy('h1'), copy('h2'), witness('h3')]);
		expect(diskIfDown(v, 'h3')).toEqual({ fate: 'ok', text: 'Loses its witness, keeps running' });
	});

	it('cannot fail over a disk whose copies disagree', () => {
		const v = vol('a', [copy('h1'), copy('h2'), witness('h3')], {
			splitBrain: true,
			primary: 'h1',
		});
		expect(diskIfDown(v, 'h1').fate).toBe('off');
		expect(diskIfDown(v, 'h2').fate).toBe('weak');
	});

	it('never reads a disk that is already down as unaffected', () => {
		for (const v of [
			vol('a', [copy('h1'), copy('h2'), witness('h3')], { quorumLost: true, phase: 'Degraded' }),
			vol('b', [copy('h1')], { phase: 'Failed' }),
			vol('c', [copy('h1', 'unreachable'), copy('h2', 'behind')], { phase: 'Degraded' }),
		]) {
			expect(diskIfDown(v, 'h9')).toEqual({ fate: 'off', text: 'Already offline' });
		}
		expect(rowIfDown(row('vm', 'h2', vol('b', [copy('h1')], { phase: 'Failed' })), 'h9').text).toBe(
			'Already offline',
		);
	});

	it('does not count a disk still being set up as lost', () => {
		expect(diskIfDown(vol('a', [copy('h1', 'unknown')], { phase: 'Provisioning' }), 'h1')).toEqual({
			fate: 'ok',
			text: 'Still being set up',
		});
	});

	it('restarts a VM next to its data', () => {
		const r = row('vm', 'h1', vol('a', [copy('h1'), copy('h2'), witness('h3')]));
		expect(rowIfDown(r, 'h1')).toEqual({ fate: 'restart', text: 'Restarts, data on h2' });
	});

	it('reads a VM as offline when one of its disks is', () => {
		const r = row('vm', 'h2', vol('a', [copy('h1')]), vol('b', [copy('h2'), copy('h3')]));
		expect(rowIfDown(r, 'h1').fate).toBe('off');
	});

	it('sorts every row into one outcome and says what maintenance costs', () => {
		const rows = [
			row('off', 'h2', vol('a', [copy('h1')])),
			row('moves', 'h1', vol('b', [copy('h1'), copy('h2'), copy('h3')])),
			row('weak', 'h3', vol('c', [copy('h1'), copy('h3'), witness('h2')])),
			row('fine', 'h3', vol('d', [copy('h2'), copy('h3'), witness('h4')])),
		];
		const r = rehearse(rows, 'h1');
		expect(r.byFate.off.map((x) => x.name)).toEqual(['off']);
		expect(r.byFate.restart.map((x) => x.name)).toEqual(['moves']);
		expect(r.byFate.weak.map((x) => x.name)).toEqual(['weak']);
		expect(r.byFate.ok.map((x) => x.name)).toEqual(['fine']);
		expect(r.maintenance).toContain('moves move away first.');
		expect(r.retire).toContain('Rebuilds 2 copies on other hosts, 2 GiB read from peers.');
		expect(r.retire).toContain('recreate off on replicated storage first');
	});
});

describe('joining volumes to VMs', () => {
	const disk = (ns: string, vm: string, claim: string, host = 'h1') =>
		({
			vm: { namespace: ns, name: vm },
			vmKey: `${ns}/${vm}`,
			project: 'proj',
			disk: { name: 'root', claim },
			host,
		}) as unknown as DiskRow;

	it('groups the disks of a VM and keeps a claim no VM uses', () => {
		const rows = mapRows(
			[
				vol('pvc-1', [copy('h1')], { namespace: 'ns', claim: 'c1' }),
				vol('pvc-2', [copy('h1')], { namespace: 'ns', claim: 'c2' }),
				vol('pvc-3', [copy('h2')], { namespace: 'ns', claim: 'orphan', primary: 'h2' }),
				vol('pvc-4', [copy('h2')]),
			],
			[disk('ns', 'vm', 'c1'), disk('ns', 'vm', 'c2')],
		);
		expect(rows.map((r) => [r.name, r.disks.length])).toEqual([
			['orphan', 1],
			['pvc-4', 1],
			['vm', 2],
		]);
		expect(rows.find((r) => r.name === 'orphan')?.runsOn).toBe('h2');
		expect(rows.find((r) => r.name === 'pvc-4')?.project).toBe('Not claimed');
	});

	it('never joins a claim of the same name in another namespace', () => {
		const rows = mapRows(
			[vol('pvc-1', [copy('h1')], { namespace: 'other', claim: 'c1' })],
			[disk('ns', 'vm', 'c1')],
		);
		expect(rows[0].vm).toBeUndefined();
	});

	it('puts a disk that needs a person first', () => {
		const rows = mapRows(
			[
				vol('a', [copy('h1')], { namespace: 'n', claim: 'a' }),
				vol('z', [copy('h1')], { namespace: 'n', claim: 'z', splitBrain: true }),
			],
			[],
		);
		expect(rows[0].name).toBe('z');
	});
});

describe('room', () => {
	const pool = (node: string, tier: string, placeable: number, available = true) =>
		({
			name: `${tier}-${node}`,
			node,
			tier,
			totalBytes: 100,
			freeBytes: 40,
			placeableBytes: placeable,
			available,
		}) as ReplicationPool;

	it('limits a two-copy disk by the second-roomiest host', () => {
		const [nvme] = tierRooms([
			pool('h1', 'nvme', 90),
			pool('h2', 'nvme', 30),
			pool('h3', 'nvme', 10),
		]);
		expect(nvme).toMatchObject({ tier: 'nvme', largest: 30, usedBytes: 180, totalBytes: 300 });
	});

	it('counts one host once and skips a pool that is not available', () => {
		const [t] = tierRooms([pool('h1', 't', 90), pool('h1', 't', 80), pool('h2', 't', 70, false)]);
		expect(t.largest).toBe(0);
	});

	it('lists every host once, sorted', () => {
		expect(hostsOf([vol('a', [copy('h3')])], [pool('h2', 't', 1)], ['h1', 'h2'])).toEqual([
			'h1',
			'h2',
			'h3',
		]);
	});
});

describe('the map', () => {
	const two = vol('a', [copy('h1'), copy('h2', 'catching-up'), witness('h3')], { phase: 'Ready' });

	it('draws each replica', () => {
		expect(cellFor(two, 'h1', 'h1').kind).toBe('copy');
		expect(cellFor(two, 'h3', 'h1').kind).toBe('witness');
		expect(cellFor(two, 'h4', 'h1').kind).toBe('');
	});

	it('marks a VM reading over the network', () => {
		expect(cellFor(two, 'h4', 'h4')).toMatchObject({ kind: 'remote' });
	});

	it('names the worst fact about a disk first', () => {
		expect(diskNow({ ...two, splitBrain: true }, 'h1').tone).toBe('danger');
		expect(diskNow(vol('a', [copy('h1'), copy('h2')], { phase: 'Ready' }), 'h3').text).toBe(
			'2 copies in sync, reads over the network',
		);
		expect(
			diskNow(vol('a', [copy('h1'), copy('h2', 'unreachable')], { phase: 'Degraded' }), 'h1').text,
		).toBe('Copy on h2 is out of reach');
	});

	it('does not count a disk whose witness is gone as surviving a host', () => {
		const p = protectionOf([
			row('thin', 'h1', vol('a', [copy('h1'), copy('h2'), witness('h3', 'unreachable')])),
		]);
		expect(p).toMatchObject({ survive: 0, segments: ['thin'] });
	});

	it('does not count two copies with no witness as surviving a host', () => {
		expect(protectionOf([row('pair', 'h1', vol('a', [copy('h1'), copy('h2')]))])).toMatchObject({
			survive: 0,
			segments: ['thin'],
		});
	});

	it('never counts a disk whose copies disagree as safe', () => {
		const p = protectionOf([
			row('split', 'h1', vol('a', [copy('h1'), copy('h2')], { splitBrain: true })),
		]);
		expect(p).toMatchObject({ survive: 0, total: 1, segments: ['trouble'] });
		expect(p.notes[0]).toContain('two copies disagree');
	});

	it('counts only what survives a host and warns while a copy catches up', () => {
		const p = protectionOf([
			row('good', 'h1', vol('b', [copy('h1'), copy('h2'), witness('h3')])),
			row('risk', 'h1', {
				...two,
				replicas: [copy('h1'), { ...copy('h2', 'catching-up'), syncPercent: 64 }],
			}),
			row('solo', 'h1', vol('c', [copy('h1')])),
		]);
		expect(p).toMatchObject({ survive: 1, total: 3, segments: ['safe', 'thin', 'local'] });
		expect(p.notes[0]).toBe(
			'risk disk: the copy on h2 is catching up, 64%. Until then, losing h1 takes it offline.',
		);
		expect(p.notes[1]).toBe('1 disk keeps one copy by choice: solo.');
	});
});

describe('what a tenant reads', () => {
	const p = (standing: string, extra: Partial<DiskProtection> = {}) =>
		({ namespace: 'n', claim: 'c', copies: 2, inSync: 2, standing, ...extra }) as DiskProtection;

	it('states what the protection means', () => {
		const said = (copies: number) => standingOf(p('protected', { copies })).detail;
		expect(said(2)).toBe('Survives losing any one host.');
		expect(said(3)).toBe('Survives losing any one host.');
		expect(said(5)).toBe('Survives losing any 2 hosts.');
	});

	it('does not call a full set of copies safe when a majority is one host away', () => {
		const out = standingOf(p('exposed', { copies: 2, inSync: 2 }));
		expect(out.detail).toContain('would not survive losing a host');
	});

	it('carries a next step on every problem and names no host', () => {
		for (const s of ['exposed', 'needs-operator']) {
			const out = standingOf(p(s));
			expect(out.detail).toMatch(/platform team|Ask them/);
			expect(JSON.stringify(out)).not.toMatch(/host-/);
		}
	});
});

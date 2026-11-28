import { describe, expect, it } from 'vitest';
import type { Desktop, DesktopClaim, DesktopPool, Desktops, Project } from './api';
import { byProject, count, joinClaims, person, poolState, unlistedLines } from './desktops';

const desktop = (name: string, over: Partial<Desktop> = {}): Desktop => ({
	namespace: 'web-prod',
	name,
	running: true,
	...over,
});
const claim = (name: string, over: Partial<DesktopClaim> = {}): DesktopClaim => ({
	namespace: 'web-prod',
	name,
	user: name,
	running: true,
	...over,
});
const pool = (over: Partial<DesktopPool> = {}): DesktopPool => ({
	namespace: 'web-prod',
	name: 'eng',
	class: 'office',
	seats: 4,
	keepWarm: 1,
	assignment: 'Personal',
	counts: { held: 0, inSession: 0, ready: 0, stopped: 0, warm: 0, free: 0, waiting: 0 },
	desktops: [],
	...over,
});

describe('joinClaims', () => {
	it('pairs a pool desktop with the claim the server named on it', () => {
		const alice = claim('eng-alice', { user: 'alice', pool: 'eng', phase: 'Bound' });
		const rows = joinClaims(
			[desktop('eng-1', { pool: 'eng', claim: 'eng-alice' }), desktop('eng-2', { pool: 'eng' })],
			[alice, claim('eng-bob', { pool: 'eng', phase: 'Waiting' })],
		);
		expect(rows.map((r) => r.claim)).toEqual([alice, undefined]);
		expect(rows.map(person)).toEqual(['alice', '']);
	});
	it('never pairs a claim of the same name in another namespace', () => {
		const [row] = joinClaims(
			[desktop('eng-1', { claim: 'eng-alice' })],
			[claim('eng-alice', { namespace: 'db-prod' })],
		);
		expect(row.claim).toBeUndefined();
	});
	it('names the owner when the claims are not shown', () => {
		const [row] = joinClaims([desktop('carol-dev', { owner: 'carol', claim: 'gone' })], []);
		expect(row.claim).toBeUndefined();
		expect(person(row)).toBe('carol');
	});
});

describe('pools', () => {
	it('reads a count DeskuBe left out as 0', () => {
		const p = pool({ counts: { waiting: 2 } as DesktopPool['counts'] });
		expect(count(p, 'waiting')).toBe(2);
		expect(count(p, 'held')).toBe(0);
	});
	it('names the missing class, and never a closed pool as a fault', () => {
		const missing = pool({
			class: 'gpu',
			conditions: [{ type: 'Ready', status: 'False', reason: 'ClassNotFound' }],
		});
		expect(poolState(missing)).toMatchObject({ tone: 'danger', missingClass: 'gpu' });
		const closed = pool({
			closed: true,
			conditions: [{ type: 'Ready', status: 'True', reason: 'ClassFound' }],
		});
		expect(poolState(closed)).toEqual({ tone: 'neutral', label: 'Closed' });
		expect(poolState(pool()).label).toBe('Not read yet');
	});
});

describe('unlisted kinds', () => {
	const unlisted = [
		{ namespace: 'batch-prod', resource: 'vdiinstances' },
		{ namespace: 'db-prod', resource: 'vdiinstances' },
		{ namespace: 'db-prod', resource: 'vdiclaims' },
	];
	it('says each kind once, where it is not shown', () => {
		expect(unlistedLines(unlisted, null)).toEqual([
			'Desktops in batch-prod, db-prod are not shown to you.',
			'Claims in db-prod are not shown to you.',
		]);
		expect(unlistedLines(unlisted, new Set(['batch-prod']), true)).toEqual([
			'Desktops here are not shown to you.',
		]);
	});
	it('lists no namespace for a kind not listed alone, and counts none it cannot', () => {
		const data: Desktops = {
			synced: true,
			classes: [],
			pools: [pool(), pool({ namespace: 'batch-prod', name: 'render' })],
			desktops: [desktop('ops', { namespace: 'db-prod' })],
			claims: [],
			unlisted: [
				{ namespace: 'batch-prod', resource: 'vdiinstances' },
				{ namespace: 'qa', resource: 'vdiinstances' },
			],
		};
		const projects: Project[] = [
			{
				name: 'team',
				namespaces: ['web-prod', 'db-prod', 'batch-prod', 'qa'].map((namespace) => ({
					namespace,
					vms: [],
				})),
			},
		];
		const [team] = byProject(data, projects);
		expect(team.namespaces.map((n) => [n.namespace, n.count])).toEqual([
			['web-prod', 0],
			['db-prod', 1],
			['batch-prod', null],
		]);
		expect(team.count).toBe(1);
	});
});

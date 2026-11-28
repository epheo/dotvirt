import { describe, expect, it } from 'vitest';
import type { Desktop, DesktopClaim, DesktopPool, Desktops, Project } from './api';
import {
	byProject,
	count,
	duration,
	joinClaims,
	person,
	poolState,
	rights,
	seatParts,
	unlistedLines,
} from './desktops';

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
	it('names the missing class; closed is no state of Ready', () => {
		const missing = pool({
			class: 'gpu',
			conditions: [{ type: 'Ready', status: 'False', reason: 'ClassNotFound' }],
		});
		expect(poolState(missing)).toMatchObject({ tone: 'danger', missingClass: 'gpu' });
		const closed = pool({
			closed: true,
			conditions: [{ type: 'Ready', status: 'True', reason: 'ClassFound' }],
		});
		expect(poolState(closed)).toEqual({ tone: 'ok', label: 'Ready' });
		expect(poolState(pool()).label).toBe('Not read yet');
	});
	it('splits the seats into parts that never overlap', () => {
		const p = pool({
			seats: 6,
			counts: { held: 4, inSession: 1, ready: 3, stopped: 1, warm: 1, free: 1, waiting: 2 },
		});
		const parts = seatParts(p);
		expect(parts).toEqual({ inSession: 1, idle: 2, stopped: 1, warm: 1, free: 1 });
		expect(Object.values(parts).reduce((n, v) => n + v, 0)).toBe(4 + 1 + 1);
		// Counts read mid-update never make a part negative.
		expect(
			seatParts(pool({ counts: { held: 1, inSession: 1, stopped: 1 } as DesktopPool['counts'] }))
				.idle,
		).toBe(0);
	});
	it('says a duration for people', () => {
		expect(duration('30m0s')).toBe('30 min');
		expect(duration('12h0m0s')).toBe('12 h');
		expect(duration('1h30m0s')).toBe('1 h 30 min');
		expect(duration('720h0m0s')).toBe('30 d');
		expect(duration('0s')).toBe('0 s');
		expect(duration('1.5s')).toBe('1.5s');
	});
});

describe('unlisted kinds', () => {
	const unlisted = [
		{ namespace: '', resource: 'vdiclasses' },
		{ namespace: 'batch-prod', resource: 'vdiinstances' },
		{ namespace: 'db-prod', resource: 'vdiclaims' },
		{ namespace: 'db-prod', resource: 'vdiinstances' },
	];
	it('says each kind once, where it is not shown, and who may let the caller read it', () => {
		expect(unlistedLines(unlisted, null)).toEqual([
			'Desktops in batch-prod, db-prod are not shown to you: a platform admin can let you read them.',
			'Claims in db-prod are not shown to you: a project admin can let you read them.',
		]);
		expect(unlistedLines(unlisted, new Set(['batch-prod']), true)).toEqual([
			'Desktops here are not shown to you: a platform admin can let you read them.',
		]);
	});
	it('lists only what the answer covers and does not name', () => {
		const data = { unlisted } as Desktops;
		const may = rights(data, new Set(['web-prod', 'db-prod', 'batch-prod']));
		expect(may.lists('web-prod', 'vdiinstances')).toBe(true);
		expect(may.lists('db-prod', 'vdiinstances')).toBe(false);
		expect(may.lists('db-prod', 'vdipools')).toBe(true);
		// A namespace the answer does not cover is listed nowhere.
		expect(may.lists('qa', 'vdipools')).toBe(false);
		expect(may.classes).toBe(false);
		expect(may.anywhere('vdiinstances', ['db-prod', 'batch-prod'])).toBe(false);
		expect(may.anywhere('vdiinstances')).toBe(true);
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
				{ namespace: 'qa', resource: 'vdiclaims' },
				{ namespace: 'qa', resource: 'vdiinstances' },
				{ namespace: 'qa', resource: 'vdipools' },
			],
		};
		const namespaces = ['web-prod', 'db-prod', 'batch-prod', 'qa'];
		const projects: Project[] = [
			{ name: 'team', namespaces: namespaces.map((namespace) => ({ namespace, vms: [] })) },
		];
		const [team] = byProject(data, projects, rights(data, new Set(namespaces)));
		expect(team.namespaces.map((n) => [n.namespace, n.count])).toEqual([
			['web-prod', 0],
			['db-prod', 1],
			['batch-prod', null],
		]);
		expect(team.count).toBe(1);
	});
});

import { describe, expect, it } from 'vitest';
import type { DiskProtection, ReplicatedVolume, Route, VolumeReplica } from '#lib/api.js';
import { protectionTile, replicationTile, routesTile } from './landing';
import type { MapRow } from './replication';

const copy = (node: string): VolumeReplica => ({ node, role: 'diskful', state: 'in-sync' });
const witness = (node: string): VolumeReplica => ({ node, role: 'tiebreaker', state: 'standby' });
const vol = (replicas: VolumeReplica[], extra: Partial<ReplicatedVolume> = {}) =>
	({
		name: 'v',
		phase: 'Ready',
		sizeBytes: 1,
		copies: replicas.filter((r) => r.role === 'diskful').length,
		tier: 'nvme',
		replicas,
		...extra,
	}) as ReplicatedVolume;
const row = (name: string, volume: ReplicatedVolume): MapRow => ({
	key: `ns/${name}`,
	name,
	project: 'p',
	runsOn: 'h1',
	disks: [{ label: 'disk', volume }],
});
const safe = (name: string) => row(name, vol([copy('h1'), copy('h2'), witness('h3')]));
// Two copies, one still catching up: not surviving h1 yet, mending by itself.
const mending = (name: string) =>
	row(
		name,
		vol([
			copy('h1'),
			{ node: 'h2', role: 'diskful', state: 'catching-up', syncPercent: 40 },
			witness('h3'),
		]),
	);

describe('the replication tile', () => {
	it('says what survives and stays untinted when all does', () => {
		expect(replicationTile([safe('a'), safe('b')])).toMatchObject({
			value: '2 of 2',
			label: 'disks survive losing a host',
			tone: 'none',
			link: { href: '/storage?tab=replication' },
		});
	});

	it('lands on the disk in trouble and says what is wrong with that disk', () => {
		const t = replicationTile([
			mending('mending'),
			row('thin', vol([copy('h1'), copy('h2')])),
			row('split', vol([copy('h1'), copy('h2')], { splitBrain: true })),
			safe('ok'),
		]);
		expect(t).toMatchObject({ value: '1 of 4', tone: 'danger', link: { label: 'Show split' } });
		expect(t.link.href).toBe('/storage?tab=replication&focus=ns%2Fsplit');
		expect(t.body).toBe('split disk: Two copies disagree.');
	});

	it('warns on a disk a lost host takes offline, saying which host', () => {
		const t = replicationTile([safe('ok'), row('thin', vol([copy('h1'), copy('h2')]))]);
		expect(t).toMatchObject({ tone: 'warn', link: { label: 'Show thin' } });
		expect(t.body).toBe('thin disk: Loses its majority with h1.');
	});

	it('counts a copy catching up short, untinted, as the owners read it', () => {
		const t = replicationTile([safe('ok'), mending('mending')]);
		expect(t).toMatchObject({
			value: '1 of 2',
			tone: 'none',
			link: { href: '/storage?tab=replication' },
		});
		expect(t.body).toContain('catching up, 40%');
	});

	it('claims nothing about no disk', () => {
		expect(replicationTile([])).toMatchObject({ label: 'No replicated disks', tone: 'none' });
		expect(replicationTile([]).value).toBeUndefined();
	});
});

describe('the protection tile', () => {
	const disk = (claim: string, standing: string): DiskProtection => ({
		namespace: 'web',
		claim,
		copies: 2,
		inSync: 2,
		standing,
	});
	const name = (p: DiskProtection) => p.claim;

	it('names the disk that needs the platform team first', () => {
		const t = protectionTile(
			[disk('a', 'protected'), disk('b', 'exposed'), disk('c', 'needs-operator')],
			name,
		);
		expect(t).toMatchObject({ value: '1 of 3', tone: 'danger', link: { label: 'Show c' } });
		expect(t.link.href).toBe('/storage?tab=replication&focus=web%2Fc');
		expect(t.body).toBe('c: This disk cannot recover by itself. Ask them to look at it.');
	});

	it('says a copy catching up without tint or a row to land on', () => {
		const t = protectionTile([disk('a', 'protected'), disk('b', 'catching-up')], name);
		expect(t).toMatchObject({ tone: 'none', link: { href: '/storage?tab=replication' } });
		expect(t.body).toMatch(/^b: /);
	});

	it('says why the count is short when only a choice made it so', () => {
		const t = protectionTile([disk('a', 'protected'), disk('b', 'local')], name);
		expect(t).toMatchObject({ value: '1 of 2', tone: 'none', body: 'b: One copy.' });
	});

	it('claims nothing about no disk', () => {
		expect(protectionTile([], name)).toMatchObject({ label: 'No replicated disks' });
	});
});

describe('the routes tile', () => {
	const route = (name: string, attached: boolean, reason?: string, message?: string): Route => ({
		kind: 'HTTPRoute',
		namespace: 'web',
		name,
		hostnames: [`${name}.example.com`],
		parents: [{ namespace: 'edge', name: 'public', attached, reason, message }],
	});

	it('names the first refused route by its reason, never the controller text', () => {
		const routes = [
			route('shop', true),
			route('blog', false, 'HostnameConflict', 'Taken by route web/shop'),
		];
		const t = routesTile(routes, true);
		expect(t).toMatchObject({ value: '1 of 2', label: 'routes live', tone: 'danger' });
		expect(t.body).toBe(
			'blog.example.com: Taken. Another route holds this hostname. Pick another.',
		);
		expect(t.link.href).toBe(
			'/networking?tab=gateway&gateway=edge%2Fpublic&route=HTTPRoute%2Fweb%2Fblog',
		);
		expect(routesTile(routes, false).link.href).toBe(
			'/networking?tab=gateway&route=HTTPRoute%2Fweb%2Fblog',
		);
	});

	it('says how many more are refused', () => {
		const t = routesTile(
			[
				route('shop', true),
				route('blog', false, 'HostnameConflict'),
				route('wiki', false, 'BackendNotFound'),
			],
			true,
		);
		expect(t.body).toMatch(/^blog\.example\.com: Taken\. .* 1 more refused\.$/);
	});

	it('counts a route waiting for its Gateway without tint', () => {
		const t = routesTile([route('shop', true), route('new', false, 'Pending')], true);
		expect(t).toMatchObject({ value: '1 of 2', tone: 'none' });
		expect(t.body).toBe('1 waits for the Gateway to pick it up.');
	});

	it('claims nothing about no route', () => {
		expect(routesTile([], false)).toMatchObject({ label: 'No routes yet', tone: 'none' });
	});
});

import { describe, expect, it } from 'vitest';
import type { DraftItem, Project, Snapshot, StorageClassInfo, VM } from '#lib/api.js';
import {
	ago,
	applyFilters,
	classFacts,
	classTag,
	diskRows,
	diskShared,
	filtersFromQuery,
	filtersToQuery,
	imageDeleteConsequence,
	imageProgress,
	imageUsage,
	importUsage,
	libraryTiles,
	moveConsequences,
	moveNote,
	planBulkMove,
	planBulkSnapshot,
	settleEach,
	uploadAbility,
	suggestImageName,
	projectsBySize,
	repeatedNames,
	sortRows,
	sourceKey,
	sourceOf,
	sourceTail,
	sourceText,
	staleShare,
	storageContext,
	suggestTarget,
	topImages,
	type DiskRow,
} from '#lib/storage.js';

const NOW = Date.parse('2026-11-07T12:00:00Z');
const hoursAgo = (h: number) => new Date(NOW - h * 3_600_000).toISOString();

const classes: StorageClassInfo[] = [
	{
		name: 'lvms-vgslow',
		default: true,
		provisioner: 'topolvm.io',
		snapshotClass: 'lvms-vgslow',
		profile: { accessModes: ['ReadWriteOnce'], recognized: true },
		free: 412 * 2 ** 40,
	},
	{
		name: 'lvms-vgfast',
		description: 'local NVMe',
		provisioner: 'topolvm.io',
		snapshotClass: 'lvms-vgfast',
		profile: { accessModes: ['ReadWriteOnce'], recognized: true },
		free: 2 ** 30,
	},
	{
		name: 'bulk-hdd',
		provisioner: 'nfs.csi.k8s.io',
		snapshotClass: 'bulk-hdd',
		profile: { accessModes: ['ReadWriteMany'], shared: true, recognized: true },
	},
];

function vm(name: string, over: Partial<VM> = {}): VM {
	return {
		namespace: 'web-prod',
		name,
		power: 'On',
		sync: 'Synced',
		sourceFile: `web-prod/${name}.yaml`,
		phase: 'Running',
		nodeName: 'host-07',
		disks: [
			{
				name: 'rootdisk',
				type: 'dataVolume',
				size: '30Gi',
				claim: `${name}-rootdisk`,
				image: 'os-images/fedora-42',
			},
			{ name: 'cloudinitdisk', type: 'cloudInitNoCloud' },
		],
		...over,
	};
}

const projects = (...vms: VM[]): Project[] => [
	{ name: 'team-web', namespaces: [{ namespace: 'web-prod', vms }] },
];

const snap = (vmName: string, h: number, over: Partial<Snapshot> = {}): Snapshot => ({
	namespace: 'web-prod',
	vm: vmName,
	name: `${vmName}-${h}`,
	created: hoursAgo(h),
	readyToUse: true,
	volumes: ['rootdisk'],
	...over,
});

function rows(
	vms: VM[],
	snapshots: Snapshot[] | null = [],
	extra: Partial<Parameters<typeof diskRows>[0]> = {},
) {
	return diskRows({
		projects: projects(...vms),
		classes,
		defaultClass: 'lvms-vgslow',
		snapshots,
		claims: [],
		nodes: [{ name: 'host-17', ready: true, maintenance: true }],
		now: NOW,
		...extra,
	});
}

describe('ago', () => {
	it('words the age of a snapshot', () => {
		expect(ago(hoursAgo(0), NOW)).toBe('just now');
		expect(ago(hoursAgo(0.5), NOW)).toBe('30 min ago');
		expect(ago(hoursAgo(2), NOW)).toBe('2 h ago');
		expect(ago(hoursAgo(30), NOW)).toBe('1 day ago');
		expect(ago(hoursAgo(24 * 9 + 1), NOW)).toBe('9 days ago');
		expect(ago(undefined, NOW)).toBe('');
	});
});

describe('diskShared', () => {
	it('reads explicit access modes before the class profile', () => {
		expect(diskShared({ name: 'd', accessModes: ['ReadWriteMany'] }, classes[0])).toBe(true);
		expect(diskShared({ name: 'd' }, classes[0])).toBe(false);
		expect(diskShared({ name: 'd' }, classes[2])).toBe(true);
		expect(diskShared({ name: 'd' }, undefined)).toBeNull();
	});
});

describe('diskRows', () => {
	it('joins class, host, image and snapshots per persistent disk', () => {
		const r = rows([vm('web-1')], [snap('web-1', 2), snap('web-1', 300)]);
		expect(r).toHaveLength(1);
		expect(r[0]).toMatchObject({
			key: 'web-prod/web-1/rootdisk',
			project: 'team-web',
			storageClass: 'lvms-vgslow',
			shared: false,
			host: 'host-07',
			image: 'os-images/fedora-42',
			fresh: true,
			ephemeral: false,
		});
		expect(r[0].size).toBe(30 * 2 ** 30);
		expect(r[0].snapshots.map((s) => s.name)).toEqual(['web-1-2', 'web-1-300']);
	});

	it('takes a stopped local disk host from its claim, and flags maintenance', () => {
		const r = rows([vm('web-2', { phase: 'Stopped', nodeName: undefined })], [], {
			claims: [{ namespace: 'web-prod', name: 'web-2-rootdisk', node: 'host-17' }],
		});
		expect(r[0]).toMatchObject({ host: 'host-17', maintenance: true, running: false });
	});

	it('counts only snapshots that captured the disk, and only ready ones', () => {
		const r = rows(
			[vm('web-1')],
			[
				snap('web-1', 1, { volumes: ['data'] }),
				snap('web-1', 2, { readyToUse: false }),
				snap('web-1', 400),
			],
		);
		expect(r[0].snapshots.map((s) => s.name)).toEqual(['web-1-400']);
		expect(r[0].fresh).toBe(false);
	});

	it('lists a VM without persistent disks as one ephemeral row', () => {
		const r = rows([
			vm('stateless', { disks: [{ name: 'containerdisk', type: 'containerDisk' }] }),
		]);
		expect(r).toHaveLength(1);
		expect(r[0]).toMatchObject({ ephemeral: true, key: 'web-prod/stateless/containerdisk' });
	});
});

describe('filters', () => {
	const all = rows(
		[
			vm('web-1'),
			vm('web-2', { phase: 'Stopped' }),
			vm('web-3', {
				disks: [{ name: 'rootdisk', type: 'dataVolume', storageClass: 'bulk-hdd', size: '10Gi' }],
			}),
			vm('stateless', { disks: [{ name: 'containerdisk', type: 'containerDisk' }] }),
		],
		[snap('web-1', 3)],
	);

	it('round-trips through the query, keeping other parameters', () => {
		const q = new URLSearchParams('tab=disks&f=nolm,bogus,nolm&project=team-web&q=web');
		const f = filtersFromQuery(q);
		expect(f).toEqual({ flags: ['nolm'], project: 'team-web', q: 'web' });
		const back = filtersToQuery({ flags: ['nosnap'], class: 'bulk-hdd' }, q);
		expect(back.toString()).toBe('tab=disks&f=nosnap&class=bulk-hdd');
	});

	it('narrows rows, and shows ephemeral rows only under keeps-no-data', () => {
		expect(applyFilters(all, { flags: [] }).map((r) => r.vm.name)).toEqual([
			'web-1',
			'web-2',
			'web-3',
		]);
		expect(applyFilters(all, { flags: ['nolm'] }).map((r) => r.vm.name)).toEqual([
			'web-1',
			'web-2',
		]);
		expect(applyFilters(all, { flags: ['nolm', 'nosnap'] }).map((r) => r.vm.name)).toEqual([
			'web-2',
		]);
		expect(applyFilters(all, { flags: ['stopped'] }).map((r) => r.vm.name)).toEqual(['web-2']);
		expect(applyFilters(all, { flags: ['nodata'] }).map((r) => r.vm.name)).toEqual(['stateless']);
		expect(applyFilters(all, { flags: [], class: 'bulk-hdd' }).map((r) => r.vm.name)).toEqual([
			'web-3',
		]);
		expect(applyFilters(all, { flags: [], q: 'WEB-3' })).toHaveLength(1);
	});
});

describe('sorting', () => {
	it('sorts by VM with a stable tiebreak', () => {
		const r = rows([vm('web-10'), vm('web-2'), vm('web-1')]);
		expect(sortRows(r, 'vm').map((x) => x.vm.name)).toEqual(['web-1', 'web-2', 'web-10']);
		expect(sortRows(r, 'vm', true).map((x) => x.vm.name)).toEqual(['web-10', 'web-2', 'web-1']);
	});
});

describe('library', () => {
	const fedora = (name: string, over: Partial<VM> = {}) => vm(name, over);
	const centos = (name: string) =>
		vm(name, {
			disks: [
				{
					name: 'rootdisk',
					type: 'dataVolume',
					image: 'os-images/centos-stream-9',
					storageClass: 'bulk-hdd',
				},
			],
		});
	const blank = vm('data-only', {
		disks: [{ name: 'data', type: 'dataVolume', size: '1Ti' }],
	});
	const r = rows(
		[fedora('f1'), fedora('f2'), fedora('f3'), centos('c1'), blank],
		[snap('f1', 1), snap('c1', 1)],
	);

	it('counts disks per boot image, local and shared, fresh snapshots and templates', () => {
		const uses = imageUsage(
			r,
			[
				{ name: 'fedora-42', namespace: 'os-images', ready: true },
				{ name: 'rhel-9.6', namespace: 'os-images', ready: true },
			],
			[
				{
					name: 'fedora-server',
					library: 'p',
					sourceFile: '',
					yaml: '',
					image: 'os-images/fedora-42',
				},
				{
					name: 'fedora-minimal',
					library: 'p',
					sourceFile: '',
					yaml: '',
					image: 'os-images/fedora-42',
				},
			],
		);
		expect(uses.map((u) => [u.image, u.disks, u.local, u.shared, u.fresh])).toEqual([
			['os-images/fedora-42', 3, 3, 0, 1],
			['', 1, 1, 0, 0],
			['os-images/centos-stream-9', 1, 0, 1, 1],
			['os-images/rhel-9.6', 0, 0, 0, 0],
		]);
		expect(uses[0].templates).toEqual(['fedora-server', 'fedora-minimal']);
		expect(staleShare(uses[0])).toBeCloseTo(2 / 3);

		// An unused catalog image never leads, even with room at the top.
		expect(topImages(uses, 4).top.map((u) => u.image)).toEqual([
			'os-images/fedora-42',
			'os-images/centos-stream-9',
		]);
		// Past the used ones, the rest may all be unused: the fold then counts
		// images and no disks, which the row words as such.
		const rest = topImages(
			imageUsage(r, [
				{ name: 'fedora-42', namespace: 'os-images', ready: true },
				...Array.from({ length: 11 }, (_, i) => ({
					name: `idle-${i}`,
					namespace: 'os-images',
					ready: true,
				})),
			]),
			4,
		);
		expect(rest.top.map((u) => u.image)).toEqual([
			'os-images/fedora-42',
			'os-images/centos-stream-9',
		]);
		expect(rest.more).toMatchObject({ count: 11, disks: 0, local: 0, shared: 0 });
		const sum = topImages(uses, 1);
		expect(sum.top.map((u) => u.image)).toEqual(['os-images/fedora-42']);
		expect(sum.more).toMatchObject({ count: 2, disks: 1, shared: 1, fresh: 1 });
		expect(sum.none?.disks).toBe(1);
		expect(sum.total).toBe(3);
	});

	it('derives the tiles, with no stale answer before snapshots land', () => {
		const withData = [...r, ...rows([vm('s', { disks: [{ name: 'c', type: 'containerDisk' }] })])];
		const t = libraryTiles(withData, classes, true);
		expect(t).toMatchObject({ disks: 5, noLiveMigrate: 4, stale: 3, noData: 1 });
		expect(libraryTiles(withData, classes, false).stale).toBeNull();
		expect(libraryTiles([], classes, true)).toMatchObject({ disks: 0, noLiveMigrate: 0, stale: 0 });
		const fast = t.free.find((f) => f.name === 'lvms-vgfast')!;
		expect(fast).toMatchObject({ free: 2 ** 30, held: 0, nearFull: false });
		expect(t.free.find((f) => f.name === 'bulk-hdd')!.free).toBeNull();
		const tight = libraryTiles(
			rows([
				vm('big', {
					disks: [{ name: 'r', type: 'dataVolume', storageClass: 'lvms-vgfast', size: '20Gi' }],
				}),
			]),
			classes,
			true,
		);
		expect(tight.free.find((f) => f.name === 'lvms-vgfast')!.nearFull).toBe(true);
	});

	it('words the header context', () => {
		expect(storageContext(r, 3, 48)).toBe('5 disks on 3 classes, 48 hosts');
		expect(storageContext(r.slice(0, 1), 1, 0)).toBe('1 disk on 1 class');
		expect(storageContext([], 2, 3, false)).toBe('2 classes, 3 hosts');
	});
});

describe('imported disks', () => {
	const pulled = (name: string, source: string, over: Partial<VM> = {}) =>
		vm(name, {
			disks: [
				{
					name: 'rootdisk',
					type: 'dataVolume',
					size: name === 'ci-3' ? '40Gi' : '30Gi',
					claim: `${name}-rootdisk`,
					import: { kind: 'registry', source },
				},
			],
			...over,
		});
	const signed = vm('db-1', {
		disks: [
			{
				name: 'data',
				type: 'dataVolume',
				size: '100Gi',
				storageClass: 'bulk-hdd',
				import: { kind: 'url', source: 'https://dl.example/seed.qcow2', redacted: true },
			},
		],
	});
	const blank = vm('scratch', { disks: [{ name: 'data', type: 'dataVolume', size: '1Ti' }] });
	const fedora = 'quay.io/containerdisks/fedora:42';
	const r = rows(
		[
			pulled('ci-1', fedora),
			pulled('ci-2', fedora),
			pulled('ci-3', fedora),
			pulled('qa-1', 'quay.io/containerdisks/centos-stream:9'),
			signed,
			blank,
			vm('web-1'),
		],
		[snap('ci-1', 1)],
	);

	it('counts imports apart from boot images and from blank disks', () => {
		expect(imageUsage(r).map((u) => [u.image, u.disks])).toEqual([
			['', 1],
			['os-images/fedora-42', 1],
		]);
		const { all, sources } = importUsage(r);
		expect(all).toEqual({ disks: 5, local: 4, shared: 1, fresh: 1 });
		expect(sources.map((u) => [u.key, u.disks, u.size, u.redacted])).toEqual([
			[`registry:${fedora}`, 3, '30Gi', false],
			['registry:quay.io/containerdisks/centos-stream:9', 1, '30Gi', false],
			['url:https://dl.example/seed.qcow2', 1, '100Gi', true],
		]);
		expect(importUsage(rows([vm('web-1')]))).toEqual({
			all: { disks: 0, local: 0, shared: 0, fresh: 0 },
			sources: [],
		});
	});

	it('filters on any import or on one source, from the query', () => {
		const any = filtersFromQuery(new URLSearchParams('source=any'));
		expect(applyFilters(r, any).map((x) => x.vm.name)).toEqual([
			'ci-1',
			'ci-2',
			'ci-3',
			'qa-1',
			'db-1',
		]);
		const one = filtersFromQuery(new URLSearchParams({ source: `registry:${fedora}` }));
		expect(applyFilters(r, one).map((x) => x.vm.name)).toEqual(['ci-1', 'ci-2', 'ci-3']);
		expect(filtersToQuery(one, new URLSearchParams()).get('source')).toBe(`registry:${fedora}`);
		expect(applyFilters(r, { flags: [], q: 'seed' }).map((x) => x.vm.name)).toEqual(['db-1']);
	});

	it('keys and words a source', () => {
		const imp = { kind: 'url', source: 'https://dl.example/a:b/seed.qcow2' };
		expect(sourceOf(sourceKey(imp))).toEqual(imp);
		expect(sourceText(imp)).toBe('dl.example/a:b/seed.qcow2');
		expect(sourceText({ kind: 'registry', source: fedora })).toBe(fedora);
		expect(sourceTail(imp)).toBe('seed.qcow2');
		expect(sourceTail({ kind: 'registry', source: fedora })).toBe('fedora:42');
		expect(sourceTail({ kind: 'url', source: 'https://dl.example/' })).toBe('dl.example');
	});
});

describe('tree and class facts', () => {
	it('orders projects by VM count', () => {
		const ps: Project[] = [
			{ name: 'small', namespaces: [{ namespace: 'a', vms: [vm('x')] }] },
			{ name: 'big', namespaces: [{ namespace: 'b', vms: [vm('y'), vm('z')] }] },
		];
		expect(projectsBySize(ps)).toEqual([
			{ name: 'big', vms: 2 },
			{ name: 'small', vms: 1 },
		]);
	});

	it('tags classes and states what they allow', () => {
		expect(classTag(classes[0])).toBe('local, default');
		expect(classTag(classes[2])).toBe('shared');
		expect(classFacts(classes[0])).toMatchObject({
			liveMigration: false,
			snapshots: true,
			free: '412 TiB free',
		});
		expect(classFacts(classes[2]).free).toBe('Not reported');
		expect(classFacts(undefined).liveMigration).toBeNull();
		expect(suggestTarget(classes)).toBe('bulk-hdd');
	});
});

describe('planBulkMove', () => {
	const fast = (name: string) =>
		vm(name, {
			disks: [{ name: 'rootdisk', type: 'dataVolume', storageClass: 'lvms-vgfast', size: '20Gi' }],
		});
	const twoDisks = vm('two', {
		disks: [
			{ name: 'rootdisk', type: 'dataVolume', size: '30Gi' },
			{ name: 'data', type: 'dataVolume', size: '100Gi' },
		],
	});
	const all = rows([
		vm('web-1'),
		vm('web-2', { phase: 'Stopped' }),
		fast('cache-1'),
		twoDisks,
		vm('adopted', { sourceFile: '' }),
	]);
	const staged = new Map<string, DraftItem>([
		[
			'web-prod/web-1',
			{
				kind: 'edit',
				namespace: 'web-prod',
				name: 'web-1',
				changes: [],
				edit: { memory: '8Gi', migrateVolumes: [{ name: 'rootdisk', storageClass: 'bulk-hdd' }] },
			},
		],
	]);
	const pick = (...keys: string[]) => all.filter((r) => keys.includes(r.key));

	it('composes one edit per VM and sorts out what cannot move', () => {
		const plan = planBulkMove(
			pick(
				'web-prod/web-1/rootdisk',
				'web-prod/web-2/rootdisk',
				'web-prod/cache-1/rootdisk',
				'web-prod/two/rootdisk',
				'web-prod/adopted/rootdisk',
			),
			'bulk-hdd',
			{ classes, staged, rows: all },
		);
		expect(plan.already.map((r) => r.vm.name)).toEqual(['web-1']);
		expect(plan.stopped.map((r) => r.vm.name)).toEqual(['web-2']);
		expect(plan.blocked.map((b) => [b.row.vm.name, b.reason])).toEqual([['adopted', 'not in git']]);
		expect(plan.edits.map((e) => [e.vm.name, e.edit.migrateVolumes])).toEqual([
			['cache-1', [{ name: 'rootdisk', storageClass: 'bulk-hdd' }]],
			['two', [{ name: 'rootdisk', storageClass: 'bulk-hdd' }]],
		]);
		expect(plan).toMatchObject({ moving: 2, vms: 2, projects: ['team-web'], targetFree: null });
		// two keeps its local data disk, so only cache-1 live-migrates after.
		expect(plan.liveMigrateAfter).toBe(1);
		expect(plan.added).toBe(50 * 2 ** 30);
		// Leaving the default class is the expected move; leaving a picked one is news.
		expect(plan.leaving).toEqual([
			{ storageClass: 'lvms-vgfast', count: 1, description: 'local NVMe' },
		]);

		expect(moveNote(plan)).toBe(
			'Adds 2 moves to the team-web draft. web-1 rootdisk is already in it.',
		);
		expect(moveConsequences(plan)).toEqual([
			{
				tone: 'warn',
				text: '1 of 2 VMs can live-migrate once it merges; the rest keep a local disk.',
			},
			{ tone: 'info', text: 'Running VMs keep running while their disk copies.' },
			{ tone: 'warn', text: '1 leaves local NVMe on lvms-vgfast.' },
			{ tone: 'warn', text: 'bulk-hdd does not report free space. This adds 50 GiB.' },
			{
				tone: 'danger',
				text: '1 disk of stopped VMs is left out: a disk copies only while its VM runs.',
			},
			{ tone: 'warn', text: '1 disk is left out: adopted rootdisk is not in git.' },
		]);
	});

	it('keeps what a VM already has staged when adding a move', () => {
		const plan = planBulkMove(pick('web-prod/two/data'), 'bulk-hdd', {
			classes,
			staged: new Map([
				[
					'web-prod/two',
					{
						kind: 'edit',
						namespace: 'web-prod',
						name: 'two',
						changes: [],
						edit: {
							power: 'Off',
							migrateVolumes: [{ name: 'rootdisk', storageClass: 'bulk-hdd' }],
						},
					},
				],
			]),
			rows: all,
		});
		expect(plan.edits[0].edit).toEqual({
			power: 'Off',
			migrateVolumes: [
				{ name: 'rootdisk', storageClass: 'bulk-hdd' },
				{ name: 'data', storageClass: 'bulk-hdd' },
			],
		});
		expect(plan.liveMigrateAfter).toBe(1);
		expect(moveConsequences(plan)[0]).toEqual({
			tone: 'ok',
			text: 'The VM can live-migrate once it merges.',
		});
	});

	it('refuses a VM whose staged change is not a field edit, and warns on a full target', () => {
		const plan = planBulkMove(
			pick('web-prod/cache-1/rootdisk', 'web-prod/web-1/rootdisk'),
			'lvms-vgfast',
			{
				classes,
				staged: new Map([
					['web-prod/web-1', { kind: 'delete', namespace: 'web-prod', name: 'web-1', changes: [] }],
				]),
				rows: all,
			},
		);
		expect(plan.blocked.map((b) => b.reason)).toEqual([
			'a staged delete of web-1 is waiting',
			'already on lvms-vgfast',
		]);
		const big = planBulkMove(pick('web-prod/two/data'), 'lvms-vgfast', {
			classes,
			staged: new Map(),
			rows: all,
		});
		expect(moveConsequences(big)).toContainEqual({
			tone: 'danger',
			text: 'lvms-vgfast has 1 GiB free. This adds 100 GiB.',
		});
	});
});

describe('planBulkSnapshot', () => {
	it('takes one snapshot per VM and names disks the class cannot snapshot', () => {
		const noSnap: StorageClassInfo = { ...classes[2], name: 'plain', snapshotClass: '' };
		const r: DiskRow[] = rows([
			vm('a', {
				disks: [
					{ name: 'rootdisk', type: 'dataVolume' },
					{ name: 'scratch', type: 'dataVolume', storageClass: 'plain' },
				],
			}),
			vm('b'),
		]);
		const plan = planBulkSnapshot(r, [...classes, noSnap]);
		expect(plan.vms.map((v) => v.name)).toEqual(['a', 'b']);
		expect(plan.disks).toBe(2);
		expect(plan.unsupported.map((x) => x.disk.name)).toEqual(['scratch']);
	});
});

describe('settleEach', () => {
	it('bounds concurrency and settles in item order', async () => {
		let running = 0;
		let peak = 0;
		const out = await settleEach(
			[1, 2, 3, 4, 5, 6],
			async (n) => {
				running++;
				peak = Math.max(peak, running);
				await new Promise((r) => setTimeout(r, 5));
				running--;
				if (n === 4) throw new Error('four');
				return n * 10;
			},
			2,
		);
		expect(peak).toBe(2);
		expect(
			out.map((r) => (r.status === 'fulfilled' ? r.value : (r.reason as Error).message)),
		).toEqual([10, 20, 30, 'four', 50, 60]);
		expect(await settleEach([], async () => 1)).toEqual([]);
	});
});

describe('repeatedNames', () => {
	it('flags a name only when it spans namespaces', () => {
		const items = [
			{ namespace: 'qa-a', name: 'test' },
			{ namespace: 'qa-b', name: 'test' },
			{ namespace: 'web-prod', name: 'web-1' },
			// Two disks of one VM repeat its name in one namespace: not ambiguous.
			{ namespace: 'web-prod', name: 'web-1' },
		];
		expect(
			repeatedNames(
				items,
				(i) => i.namespace,
				(i) => i.name,
			),
		).toEqual(new Set(['test']));
		expect(
			repeatedNames(
				[],
				() => '',
				() => '',
			),
		).toEqual(new Set());
	});
});

describe('uploadAbility', () => {
	const caps: Record<string, { bootImageCreate?: boolean; dataVolumeCreate?: boolean }> = {
		a: {},
		b: { dataVolumeCreate: true },
		c: { dataVolumeCreate: true, bootImageCreate: true },
		d: { dataVolumeCreate: true, bootImageCreate: true },
	};
	it('finds the first namespace for each and stops at a boot image', async () => {
		const asked: string[] = [];
		const ask = async (ns: string) => {
			asked.push(ns);
			if (ns === 'x') throw new Error('review failed');
			return caps[ns];
		};
		expect(await uploadAbility(['a', 'b', 'c', 'd', 'e'], ask, 2)).toEqual({
			image: 'c',
			upload: 'b',
			import: 'c',
		});
		expect(asked).toEqual(['a', 'b', 'c', 'd']);
		// A failed or missing verdict reads as no.
		expect(await uploadAbility(['x', 'a', 'zz'], ask)).toEqual({
			image: '',
			upload: '',
			import: '',
		});
		expect(await uploadAbility(['x', 'b'], ask)).toEqual({ image: '', upload: 'b', import: '' });
	});

	it('with upload off, stops at the first namespace the cluster may import into', async () => {
		const asked: string[] = [];
		const off: Record<string, { bootImageImport?: boolean }> = {
			a: {},
			b: {},
			c: { bootImageImport: true },
		};
		const ask = async (ns: string) => (asked.push(ns), off[ns]);
		expect(await uploadAbility(['a', 'b', 'c', 'd', 'e', 'f'], ask, 1, true)).toEqual({
			image: '',
			upload: '',
			import: 'c',
		});
		expect(asked).toEqual(['a', 'b', 'c']);
	});
});

describe('suggestImageName', () => {
	it('names a boot image after its repository and tag, or its file', () => {
		expect(suggestImageName('registry', 'quay.io/containerdisks/fedora:42')).toBe('fedora-42');
		expect(suggestImageName('registry', 'docker://quay.io/containerdisks/centos-stream:9')).toBe(
			'centos-stream-9',
		);
		expect(suggestImageName('registry', 'registry.example:5000/vm/rhel:latest')).toBe('rhel');
		expect(suggestImageName('registry', `quay.io/x/fedora@sha256:${'ab'.repeat(32)}`)).toBe(
			'fedora',
		);
		expect(
			suggestImageName('url', 'https://images.example.org/ubuntu/noble-server-cloudimg-amd64.img'),
		).toBe('noble-server-cloudimg-amd64');
		expect(suggestImageName('url', 'http://files.lab/Win2019_Eval.qcow2.xz?sig=1')).toBe(
			'win2019-eval',
		);
		expect(suggestImageName('url', 'https://images.example.org/')).toBe('');
		expect(suggestImageName('url', '')).toBe('');
	});
});

describe('an unfinished boot image', () => {
	const st = (phase: string, more: { progress?: string; error?: string } = {}) => ({
		phase,
		ready: phase === 'UploadReady',
		...more,
	});

	it('says failed from the error CDI keeps while it retries, whatever the phase', () => {
		expect(
			imageProgress(st('ImportInProgress', { progress: 'N/A', error: 'no such host' }), true),
		).toEqual({ tone: 'danger', text: 'Failed: no such host' });
		expect(imageProgress(st('Failed'), true)).toEqual({ tone: 'danger', text: 'Failed' });
	});

	it('tells a download moving from an upload waiting for its file', () => {
		expect(imageProgress(st('ImportInProgress', { progress: '42.0%' }), true).text).toBe(
			'Downloading, 42.0%',
		);
		expect(imageProgress(st('ImportInProgress', { progress: 'N/A' }), true).text).toBe(
			'Downloading',
		);
		expect(imageProgress(st('ImportInProgress', { progress: '10.0%' }), false).text).toBe(
			'Importing, 10.0%',
		);
		expect(imageProgress(st('UploadReady'), false)).toEqual({
			tone: 'warn',
			text: 'Waiting for its file to upload',
		});
		expect(imageProgress(st('ImportScheduled'), true).text).toBe('Preparing storage');
		expect(imageProgress(st('CloneInProgress'), true)).toEqual({
			tone: 'none',
			text: 'CloneInProgress',
		});
	});

	it('says what a delete takes, and the VMs in git that wait on it', () => {
		expect(imageDeleteConsequence('noble', 0)).toBe(
			'Removes noble and its disk. It never finished, so no VM was made from it.',
		);
		expect(imageDeleteConsequence('noble', 0, 'download')).toBe(
			'Stops the download and removes noble and its disk. It has not finished, so no VM was made from it.',
		);
		expect(imageDeleteConsequence('noble', 1)).toBe(
			'Removes noble and its disk. 1 VM in git waits on it; its disk stays pending until a boot image named noble is ready.',
		);
		expect(imageDeleteConsequence('noble', 1200)).toBe(
			'Removes noble and its disk. 1,200 VMs in git wait on it; their disks stay pending until a boot image named noble is ready.',
		);
	});
});

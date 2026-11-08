// The `scale` scenario: a full platform as the design boards draw it, so every
// section is built and judged against thousands of objects instead of three.
// Every headline number below is a figure the boards print (brief-v2 "Scale
// data"); the generator places objects so those figures come out exactly, and
// throws at import when one drifts. Deterministic: one seeded PRNG, fixed call
// order, so the same fleet appears on every run.
//
// Snapshots are planned per VM as [count, newest age in minutes], one a day
// further back each, then served as the cluster-wide listing the Storage
// section reads (storageSnapshots) and per VM from the same list. Disk claims
// (storageClaims) name the host each local disk is bound to.

const iso = (minsAgo) => new Date(Date.now() - minsAgo * 60_000).toISOString();
const GiB = 2 ** 30;
const TiB = 2 ** 40;

// mulberry32: tiny, fast, and identical on every Node version.
function prng(seed) {
	return () => {
		seed = (seed + 0x6d2b79f5) | 0;
		let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
		t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
		return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
	};
}
const rand = prng(2846);
const int = (lo, hi) => lo + Math.floor(rand() * (hi - lo + 1));
function shuffle(a) {
	for (let i = a.length - 1; i > 0; i--) {
		const j = Math.floor(rand() * (i + 1));
		[a[i], a[j]] = [a[j], a[i]];
	}
	return a;
}
const sum = (xs) => xs.reduce((a, b) => a + b, 0);

function check(label, got, want) {
	if (got !== want) throw new Error(`scale fixture: ${label} is ${got}, want ${want}`);
}

// Spread total over weights as integers within [lo, hi], summing exactly.
function apportion(weights, total, lo = 0, hi = Infinity) {
	const w = sum(weights);
	const out = weights.map((x) => Math.min(hi, Math.max(lo, Math.round((x * total) / w))));
	for (let i = 0, guard = 0; sum(out) !== total && guard < 1e5; i = (i + 1) % out.length, guard++) {
		const d = Math.sign(total - sum(out));
		if (out[i] + d >= lo && out[i] + d <= hi) out[i] += d;
	}
	return out;
}

// ── hosts ──────────────────────────────────────────────────────────────────────

const HOSTS = Array.from({ length: 48 }, (_, i) => `host-${String(i + 1).padStart(2, '0')}`);
const MAINTENANCE = 'host-17';
const SCHEDULABLE = HOSTS.filter((h) => h !== MAINTENANCE);
const CORES = 128;
const HOST_MEM = 1024 * GiB;

// ── projects and VM names ──────────────────────────────────────────────────────

const OTHER_PROJECTS = [
	'demo',
	'storefront',
	'checkout',
	'payments',
	'search',
	'media',
	'mobile-api',
	'identity',
	'billing',
	'crm',
	'ledger',
	'reports',
	'hr-apps',
	'ci-runners',
	'ml-train',
	'ml-serve',
	'gis',
	'iot-edge',
	'logging',
	'telemetry',
	'backup-ops',
	'qa-lab',
	'perf-lab',
	'sandbox',
	'partner-x',
	'partner-y',
	'research',
	'genomics',
	'render',
	'video',
	'chat',
	'mail',
	'docs',
	'wiki',
	'intranet',
	'vdi-pool',
	'kiosk',
	'pos-retail',
	'warehouse',
	'fleet',
	'sensors',
	'legacy-apps',
	'print',
	'edu-lab',
	'events',
	'travel',
	'support',
];
check('other projects', OTHER_PROJECTS.length, 47);

const range = (prefix, from, to) =>
	Array.from({ length: to - from + 1 }, (_, i) => `${prefix}${from + i}`);

// Each 47-more project gets 8 to 53 VMs (tenant-a's 54 stays fifth biggest);
// demo is small and named after the brief.
const otherCounts = [
	9,
	...apportion(
		OTHER_PROJECTS.slice(1).map(() => int(10, 50)),
		1347,
		8,
		53,
	),
];

const PROJECTS = [
	{
		name: 'team-batch',
		namespaces: [
			{ namespace: 'batch-prod', names: range('batch-', 1, 420) },
			{ namespace: 'batch-dev', names: range('batch-dev-', 1, 220) },
		],
	},
	{
		name: 'team-web',
		namespaces: [
			{
				namespace: 'web-prod',
				names: [
					...range('web-', 1, 389),
					...range('web-api-', 1, 8),
					...range('web-win-', 1, 4),
					...range('web-cache-', 1, 6),
					...range('web-lb-', 1, 5),
				],
			},
		],
	},
	{ name: 'analytics', namespaces: [{ namespace: 'analytics', names: range('an-', 1, 288) }] },
	{ name: 'team-db', namespaces: [{ namespace: 'db-prod', names: range('db-', 1, 96) }] },
	{
		name: 'tenant-a',
		namespaces: [
			{
				namespace: 'tenant-a',
				names: ['vm-tenant-a', 'egress-client-vm', ...range('ta-', 3, 54)],
			},
		],
	},
	...OTHER_PROJECTS.map((p, i) => ({
		name: p,
		namespaces: [
			{
				namespace: p,
				names:
					p === 'demo'
						? ['test-02', 'vm-a', 'vm-b', ...range('demo-', 4, 9)]
						: range(`${p}-`, 1, otherCounts[i]),
			},
		],
	})),
];

const vmsIn = (project) =>
	sum(PROJECTS.find((p) => p.name === project).namespaces.map((n) => n.names.length));
check('projects', PROJECTS.length, 52);
check('VMs', sum(PROJECTS.map((p) => vmsIn(p.name))), 2846);
check('team-batch VMs', vmsIn('team-batch'), 640);
check('team-web VMs', vmsIn('team-web'), 412);
check('analytics VMs', vmsIn('analytics'), 288);
check('team-db VMs', vmsIn('team-db'), 96);
check('tenant-a VMs', vmsIn('tenant-a'), 54);
check('47 more projects VMs', sum(OTHER_PROJECTS.map((p) => vmsIn(p))), 1356);

// One record per VM, filled in below and turned into model VMs at the end.
const vms = [];
const byKey = new Map();
for (const p of PROJECTS)
	for (const ns of p.namespaces)
		for (const name of ns.names) {
			const v = { project: p.name, namespace: ns.namespace, name, nics: [] };
			vms.push(v);
			byKey.set(`${ns.namespace}/${name}`, v);
		}
const get = (ns, name) => byKey.get(`${ns}/${name}`);
const inNs = (ns) => vms.filter((v) => v.namespace === ns);

// ── disks: boot images, classes, snapshot recency ──────────────────────────────

// 214 VMs keep no data (container disks): stateless batch and analytics
// workers, and the last VM of 34 smaller projects.
const containerVMs = new Set([
	...inNs('batch-dev').slice(0, 150),
	...inNs('analytics').slice(258),
	...OTHER_PROJECTS.slice(3, 37).map((p) => inNs(p).at(-1)),
]);
check('VMs without a persistent disk', containerVMs.size, 214);

// Disks made from each boot image: local / shared split and how many had a
// snapshot in the last 7 days, as the Library tab prints them.
const MORE_IMAGES = [
	'ubuntu-24.04',
	'ubuntu-22.04',
	'debian-13',
	'debian-12',
	'alma-9',
	'rocky-9',
	'rocky-8',
	'opensuse-15.6',
	'fedora-41',
	'centos-stream-10',
	'rhel-8.10',
	'rhel-10.0',
	'windows-2022',
	'windows-11',
	'alpine-3.21',
	'freebsd-14',
	'nixos-25.05',
	'cirros-0.6',
	'fedora-coreos',
	'ubuntu-20.04',
	'debian-11',
	'alma-8',
];
const moreDisks = [30, 24, 20, 17, 14, 12, 10, 9, 8, 7, 6, 5, 5, 4, 4, 3, 3, 3, 2, 2, 1, 1];
const moreShared = [3, 2, 1, 1, 1, ...Array(17).fill(0)];
const moreRecent = apportion(moreDisks, 151);
const IMAGES = [
	{ name: 'fedora-42', disks: 1904, shared: 212, recent: 1602 },
	{ name: 'centos-stream-9', disks: 812, shared: 118, recent: 641 },
	{ name: 'rhel-9.6', disks: 410, shared: 64, recent: 352 },
	{ name: 'windows-2025', disks: 96, shared: 30, recent: 55 },
	...MORE_IMAGES.map((name, i) => ({
		name,
		disks: moreDisks[i],
		shared: moreShared[i],
		recent: moreRecent[i],
	})),
];
check('boot images', IMAGES.length, 26);
check('VM disks', sum(IMAGES.map((i) => i.disks)), 3412);
check('disks on bulk-hdd', sum(IMAGES.map((i) => i.shared)), 432);
check('disks without a snapshot in 7 days', sum(IMAGES.map((i) => i.disks - i.recent)), 611);

const IMAGE_NS = 'dotvirt-images';
const DISK_SIZE = { 'fedora-42': '30Gi', 'centos-stream-9': '40Gi', 'rhel-9.6': '20Gi' };
const diskSize = (image) => DISK_SIZE[image] ?? (image.startsWith('windows') ? '80Gi' : '30Gi');
const OS = {
	'fedora-42': 'Fedora Linux 42',
	'centos-stream-9': 'CentOS Stream 9',
	'rhel-9.6': 'Red Hat Enterprise Linux 9.6',
	'windows-2025': 'Windows Server 2025',
};
const osOf = (image) => OS[image] ?? image;

// Buckets of disks sharing image, class and recency, then split into VM slots:
// 780 VMs carry a second (data) disk, so 2,632 VMs hold the 3,412 disks.
const buckets = [];
for (const img of IMAGES) {
	const local = img.disks - img.shared;
	const fast = Math.round(local * 0.105);
	const classes = [
		['lvms-vgslow', local - fast],
		['lvms-vgfast', fast],
		['bulk-hdd', img.shared],
	].filter(([, n]) => n > 0);
	const recent = apportion(
		classes.map(([, n]) => n),
		img.recent,
	);
	classes.forEach(([cls, n], i) => {
		const r = Math.min(recent[i], n);
		buckets.push({ image: img.name, cls, recent: true, n: r });
		buckets.push({ image: img.name, cls, recent: false, n: n - r });
	});
}
const DOUBLES = 780;
{
	const live = buckets.filter((b) => b.n > 1);
	const d = apportion(
		live.map((b) => b.n),
		DOUBLES,
	);
	live.forEach((b, i) => (b.doubles = Math.min(d[i], Math.floor(b.n / 2))));
	// The clamp can leave the total short; top up where room is left.
	for (const b of live)
		while (sum(live.map((x) => x.doubles)) < DOUBLES && b.doubles < Math.floor(b.n / 2))
			b.doubles++;
}
const slots = [];
for (const b of buckets) {
	const doubles = b.doubles ?? 0;
	for (let i = 0; i < b.n - 2 * doubles; i++) slots.push({ ...b, disks: 1 });
	for (let i = 0; i < doubles; i++) slots.push({ ...b, disks: 2 });
}
check('VMs with a persistent disk', slots.length, 2632);

// take pulls the first slot matching want (image/cls/recent/disks), falling back
// to the same class with any image; the pinned team-web rows read like the
// Disks tab board.
function take(want) {
	const match = (s, keys) => keys.every((k) => want[k] === undefined || s[k] === want[k]);
	let i = slots.findIndex((s) => match(s, ['image', 'cls', 'recent', 'disks']));
	if (i < 0) i = slots.findIndex((s) => match(s, ['cls', 'disks']));
	if (i < 0) throw new Error(`scale fixture: no disk slot for ${JSON.stringify(want)}`);
	return slots.splice(i, 1)[0];
}

// team-web: 389 disks on local classes, 23 on bulk-hdd, one disk per VM.
for (const v of inNs('web-prod')) {
	const n = Number(v.name.match(/(\d+)$/)[1]);
	const want = { disks: 1 };
	if (v.name.startsWith('web-api-'))
		Object.assign(want, { image: 'centos-stream-9', cls: 'lvms-vgslow' });
	else if (v.name.startsWith('web-win-'))
		Object.assign(want, { image: 'windows-2025', cls: 'lvms-vgslow', recent: false });
	else if (v.name.startsWith('web-cache-'))
		Object.assign(want, { image: 'rhel-9.6', cls: 'lvms-vgfast' });
	else if (v.name.startsWith('web-lb-'))
		Object.assign(want, { image: 'fedora-42', cls: 'bulk-hdd' });
	else if (n % 20 === 0 && n <= 360) Object.assign(want, { image: 'fedora-42', cls: 'bulk-hdd' });
	else if (n === 5) Object.assign(want, { image: 'fedora-42', cls: 'lvms-vgfast', recent: true });
	else
		Object.assign(want, {
			image: 'fedora-42',
			cls: 'lvms-vgslow',
			recent: n === 2 ? false : n <= 6 ? true : undefined,
		});
	v.slot = take(want);
}
{
	const web = inNs('web-prod');
	check('team-web local disks', web.filter((v) => v.slot.cls !== 'bulk-hdd').length, 389);
}
get('db-prod', 'db-1').slot = take({
	image: 'centos-stream-9',
	cls: 'lvms-vgslow',
	recent: true,
	disks: 2,
});
shuffle(slots);
for (const v of vms) if (!v.slot && !containerVMs.has(v)) v.slot = slots.pop();
check('unassigned disk slots', slots.length, 0);

const DISK_TOTAL = sum(vms.map((v) => v.slot?.disks ?? 0));
check('VM disks placed', DISK_TOTAL, 3412);
check(
	'disks on local classes',
	sum(vms.filter((v) => v.slot && v.slot.cls !== 'bulk-hdd').map((v) => v.slot.disks)),
	2980,
);

// ── snapshots ──────────────────────────────────────────────────────────────────

const DAY = 1440;
const snapshotPlan = {};
for (const v of vms) {
	if (!v.slot) continue;
	if (v.slot.recent) snapshotPlan[`${v.namespace}/${v.name}`] = [int(1, 8), int(30, 6.5 * DAY)];
	else if (rand() < 0.5)
		snapshotPlan[`${v.namespace}/${v.name}`] = [int(1, 4), int(8 * DAY, 40 * DAY)];
}
// The boards' own rows: web-1 has 12 named snapshots, the rest read as printed.
const WEB1_SNAPSHOTS = [
	['before-release-41', 120],
	['web-1-20261106', 1.2 * DAY],
	['before-php-8.4', 4 * DAY],
	...Array.from({ length: 9 }, (_, i) => [`web-1-auto-${i + 1}`, (5 + i * 3) * DAY]),
];
snapshotPlan['web-prod/web-1'] = [WEB1_SNAPSHOTS.length, 120];
snapshotPlan['web-prod/web-2'] = [3, 9 * DAY];
snapshotPlan['web-prod/web-3'] = [5, DAY];
snapshotPlan['web-prod/web-4'] = [5, DAY];
snapshotPlan['web-prod/web-5'] = [4, 180];
for (const w of range('web-win-', 1, 4)) snapshotPlan[`web-prod/${w}`] = [2, 12 * DAY];
{
	// Settle the total on the recent VMs, never below one each.
	const recentKeys = Object.keys(snapshotPlan).filter(
		(k) => snapshotPlan[k][1] < 7 * DAY && k !== 'web-prod/web-1',
	);
	let total = sum(Object.values(snapshotPlan).map(([c]) => c));
	for (let i = 0; total !== 9870; i = (i + 1) % recentKeys.length) {
		const p = snapshotPlan[recentKeys[i]];
		if (total > 9870 && p[0] > 1) (p[0]--, total--);
		else if (total < 9870) (p[0]++, total++);
	}
}
check('snapshots', sum(Object.values(snapshotPlan).map(([c]) => c)), 9870);
check(
	'disks without a snapshot in 7 days (by plan)',
	sum(
		vms
			.filter((v) => v.slot)
			.filter((v) => !(snapshotPlan[`${v.namespace}/${v.name}`]?.[1] < 7 * DAY))
			.map((v) => v.slot.disks),
	),
	611,
);

// ── networks ───────────────────────────────────────────────────────────────────

const networks = [];
const net = (n) => (networks.push({ sync: 'Synced', health: 'Healthy', ...n }), n);
const attach = (v, n) => {
	const ref = n.attachRef ?? n.name;
	if (!v.nics.includes(ref)) v.nics.push(ref);
};
let subnetSeq = 0;
const nextSubnet = () => {
	const i = subnetSeq++;
	return `172.${16 + Math.floor(i / 256)}.${i % 256}.0/24`;
};
const projectNet = (project, namespace, name, over = {}) =>
	net({
		name,
		kind: 'internal',
		scope: 'project',
		namespace,
		subnets: [nextSubnet()],
		attachRef: `${namespace}/${name}`,
		backing: 'UserDefinedNetwork',
		topology: 'Layer2',
		sourceFile: `networks/${name}.yaml`,
		project,
		...over,
	});

// team-web: web-net carries web-1..web-389.
const webNet = projectNet('team-web', 'web-prod', 'web-net', { subnets: ['10.40.0.0/16'] });
for (const v of inNs('web-prod').slice(0, 389)) attach(v, webNet);

// tenant-a: network-a is the namespace's primary network, so all 54 VMs ride it.
projectNet('tenant-a', 'tenant-a', 'network-a', {
	kind: 'default',
	subnets: ['10.150.0.0/16'],
	attachRef: undefined,
});

const dbRepl = projectNet('team-db', 'db-prod', 'db-repl');
const dbBackup = projectNet('team-db', 'db-prod', 'db-backup');
for (const v of range('db-', 45, 68)) attach(get('db-prod', v), dbRepl);
for (const v of range('db-', 69, 76)) attach(get('db-prod', v), dbBackup);

const anNets = ['spark-l2', 'kafka-l2', 'ingest-l2', 'analytics-mgmt'].map((n) =>
	projectNet('analytics', 'analytics', n),
);
for (const v of range('an-', 1, 60)) attach(get('analytics', v), anNets[0]);
for (const v of range('an-', 61, 90)) attach(get('analytics', v), anNets[1]);
for (const v of range('an-', 91, 110)) attach(get('analytics', v), anNets[2]);
for (const v of range('an-', 1, 10)) attach(get('analytics', v), anNets[3]);

const batchL2 = projectNet('team-batch', 'batch-prod', 'batch-l2');
const batchCtl = projectNet('team-batch', 'batch-prod', 'batch-ctl');
const batchDev = projectNet('team-batch', 'batch-dev', 'batch-dev-net');
for (const v of range('batch-', 1, 80)) attach(get('batch-prod', v), batchL2);
for (const v of range('batch-', 1, 20)) attach(get('batch-prod', v), batchCtl);
for (const v of range('batch-dev-', 151, 180)) attach(get('batch-dev', v), batchDev);

// The 47 smaller projects: two networks each, demo one (internal-l2). The
// overlap the boards flag: udn-frontend and udn-tcp-frontend share a range.
const otherNets = new Map();
for (const p of OTHER_PROJECTS) {
	if (p === 'demo') {
		otherNets.set(p, [projectNet(p, p, 'internal-l2', { subnets: ['10.20.9.0/24'] })]);
		continue;
	}
	const first =
		p === 'storefront'
			? projectNet(p, p, 'udn-frontend', { subnets: ['10.200.0.0/16'] })
			: p === 'checkout'
				? projectNet(p, p, 'udn-tcp-frontend', { subnets: ['10.200.0.0/16'] })
				: projectNet(p, p, `${p}-net`);
	otherNets.set(p, [first, projectNet(p, p, `${p}-l2`)]);
}
for (const v of ['test-02', 'demo-4', 'demo-5']) attach(get('demo', v), otherNets.get('demo')[0]);
for (const p of ['storefront', 'checkout'])
	for (const v of inNs(p).slice(0, Math.ceil(inNs(p).length / 2))) attach(v, otherNets.get(p)[0]);

// Shared and VLAN networks draw their VMs from the smaller projects.
const pool = shuffle(
	vms.filter(
		(v) =>
			OTHER_PROJECTS.includes(v.project) && !['demo', 'storefront', 'checkout'].includes(v.project),
	),
);
let poolAt = 0;
const draw = (n) => {
	const out = [];
	for (let i = 0; i < n; i++) out.push(pool[poolAt++ % pool.length]);
	return out;
};
const sharedNet = (name, members, over = {}) => {
	const n = net({
		name,
		kind: 'internal',
		scope: 'shared',
		attachRef: name,
		backing: 'ClusterUserDefinedNetwork',
		topology: 'Layer2',
		sourceFile: `shared/${name}.yaml`,
		...over,
	});
	for (const v of members) attach(v, n);
	n.namespaces = [...new Set(members.map((v) => v.namespace))].sort();
	return n;
};
const vlanNet = (name, vlan, uplink, members) =>
	sharedNet(name, members, {
		kind: 'vlan',
		vlan,
		uplink,
		topology: 'Localnet',
		subnets: undefined,
	});

sharedNet('blue-secondary', draw(86), { subnets: ['10.222.1.0/24'] });
sharedNet('green-secondary', draw(41), { subnets: ['10.222.2.0/24'] });
for (let i = 0; i < 17; i++)
	sharedNet(`shared-${String(i + 1).padStart(2, '0')}`, draw(int(2, 17)), {
		subnets: [`10.222.${i + 3}.0/24`],
	});

const lan101 = [
	...inNs('web-prod').filter((v) => {
		const n = Number(v.name.match(/^web-(\d+)$/)?.[1]);
		return n >= 7 && n <= 389 && n % 4 === 3;
	}),
	...range('db-', 21, 44).map((n) => get('db-prod', n)),
];
vlanNet('lan-101', 101, 'physnet', lan101);
vlanNet('dc-vlan-net', 300, 'dc-vlan', [get('demo', 'vm-a'), get('demo', 'vm-b'), ...draw(16)]);
vlanNet('storage-vlan', 200, 'storage-net', draw(29));
vlanNet('backup-vlan', 250, 'backup-net', draw(64));
vlanNet('lan-102', 102, 'physnet', draw(12));
vlanNet('lan-103', 103, 'physnet', draw(9));
vlanNet('mgmt-vlan', 10, 'physnet', draw(15));
vlanNet('lab-vlan', 400, 'physnet', draw(5));

// 1,370 VMs ride only the pod network. The smaller projects' own networks
// take the rest, in a fixed order, until that holds.
const primaryNs = new Set(networks.filter((n) => n.kind === 'default').map((n) => n.namespace));
const podOnly = (v) => v.nics.length === 0 && !primaryNs.has(v.namespace);
{
	let count = vms.filter(podOnly).length;
	if (count < 1370) throw new Error(`scale fixture: only ${count} pod-only VMs before calibration`);
	for (const p of OTHER_PROJECTS.slice(3))
		for (const v of inNs(p)) {
			if (count === 1370) break;
			if (!podOnly(v)) continue;
			attach(v, otherNets.get(p)[v.name.endsWith('0') ? 1 : 0]);
			count--;
		}
}
check('VMs only on the pod network', vms.filter(podOnly).length, 1370);

const countOn = (ref) => vms.filter((v) => v.nics.includes(ref)).length;
check('networks', networks.length, 131);
check('project networks', networks.filter((n) => n.scope === 'project').length, 104);
check(
	'shared networks',
	networks.filter((n) => n.scope === 'shared' && n.kind !== 'vlan').length,
	19,
);
check('VLAN networks', networks.filter((n) => n.kind === 'vlan').length, 8);
check('web-net VMs', countOn('web-prod/web-net'), 389);
check('internal-l2 VMs', countOn('demo/internal-l2'), 3);
check('lan-101 VMs', countOn('lan-101'), 120);
check('dc-vlan-net VMs', countOn('dc-vlan-net'), 18);
check('blue-secondary VMs', countOn('blue-secondary'), 86);
for (const [p, n] of [
	['team-batch', 3],
	['team-web', 1],
	['analytics', 4],
	['team-db', 2],
	['tenant-a', 1],
])
	check(`${p} networks`, networks.filter((x) => x.project === p).length, n);
for (const n of networks) delete n.project;

const uplinks = [
	{ name: 'default', bridge: 'br-ex', builtin: true, nodes: HOSTS, nodeCount: 48 },
	{
		name: 'physnet',
		bridge: 'br-physnet',
		nodes: HOSTS.slice(0, 32),
		nodeCount: 32,
		policy: 'physnet-bridge',
		sourceFile: 'uplinks/physnet-bridge.yaml',
		sync: 'Synced',
		health: 'Healthy',
	},
	{
		name: 'dc-vlan',
		bridge: 'br-dc',
		nodes: HOSTS.slice(40, 46),
		nodeCount: 6,
		policy: 'dc-vlan-bridge',
		sourceFile: 'uplinks/dc-vlan-bridge.yaml',
		sync: 'Synced',
		health: 'Degraded',
		syncError: 'FailedToConfigure on host-41: bond0 not found',
	},
	{
		name: 'storage-net',
		bridge: 'br-st',
		nodes: HOSTS,
		nodeCount: 48,
		policy: 'storage-net-bridge',
		sourceFile: 'uplinks/storage-net-bridge.yaml',
		sync: 'Synced',
		health: 'Healthy',
	},
	{
		name: 'backup-net',
		bridge: 'br-bk',
		nodes: HOSTS.slice(36),
		nodeCount: 12,
		policy: 'backup-net-bridge',
		sourceFile: 'uplinks/backup-net-bridge.yaml',
		sync: 'Synced',
		health: 'Healthy',
	},
];

const physicalAdapters = HOSTS.flatMap((h, i) => [
	{ name: 'eno1', node: h, type: 'ethernet', state: 'up', mtu: 1500, role: 'cluster-uplink' },
	{
		name: 'eno2',
		node: h,
		type: 'ethernet',
		state: 'up',
		mtu: 9000,
		role: i < 32 ? 'enslaved' : 'available',
	},
	{
		name: 'bond0',
		node: h,
		type: 'bond',
		state: h === 'host-41' ? 'down' : 'up',
		mtu: 9000,
		role: 'enslaved',
	},
]);

// ── firewall ───────────────────────────────────────────────────────────────────

const policies = [
	['deny-lateral', 10, 'Deny', 'other projects'],
	['allow-monitoring', 5, 'Allow', 'namespace openshift-monitoring'],
	['allow-dns', 8, 'Allow', 'namespace openshift-dns'],
	['deny-metadata', 20, 'Deny', '169.254.169.254/32'],
	['allow-backup-net', 30, 'Allow', 'namespace backup-ops'],
].map(([name, priority, action, peer]) => ({
	name,
	kind: 'admin',
	backing: 'AdminNetworkPolicy',
	priority,
	target: 'all namespaces',
	rules: [{ direction: 'Ingress', action, peer }],
	sourceFile: `policies/${name}.yaml`,
	sync: 'Synced',
	health: 'Healthy',
}));
policies.push({
	name: 'default',
	kind: 'baseline',
	backing: 'BaselineAdminNetworkPolicy',
	target: 'all namespaces',
	rules: [{ direction: 'Ingress', action: 'Deny', peer: 'other namespaces' }],
	sourceFile: 'policies/baseline.yaml',
	sync: 'Synced',
	health: 'Healthy',
});
check('cluster rules', policies.length, 6);

const projectRule = (namespace, name, target, peer, ports) =>
	policies.push({
		name,
		kind: 'dfw',
		namespace,
		backing: 'NetworkPolicy',
		target,
		rules: [{ direction: 'Ingress', action: 'Allow', peer, ports }],
		sourceFile: `policies/${name}.yaml`,
		sync: 'Synced',
		health: 'Healthy',
	});
projectRule('web-prod', 'allow-https', 'all VMs', 'namespace web-prod', 'TCP/443');
projectRule('db-prod', 'allow-web-to-db', 'app=db-1', 'app=web', 'TCP/5432');
{
	const ruleNs = PROJECTS.flatMap((p) => p.namespaces.map((n) => n.namespace));
	const weights = ruleNs.map((ns) => (ns === 'web-prod' ? 12 : ns === 'db-prod' ? 6 : int(2, 6)));
	const counts = apportion(weights, 212, 1);
	const PORTS = ['TCP/22', 'TCP/80', 'TCP/8080', 'TCP/9090', 'UDP/53', 'TCP/6443', 'TCP/5432'];
	ruleNs.forEach((ns, i) => {
		for (let k = 1; k <= counts[i]; k++)
			projectRule(
				ns,
				`${ns}-allow-${k}`,
				`app=${ns}-${k}`,
				`app=${ns}`,
				PORTS[(i + k) % PORTS.length],
			);
	});
}
check('project rules', policies.filter((p) => p.kind === 'dfw').length, 214);

for (const [name, ns, ip] of [
	['team-web-egress', 'web-prod', '203.0.113.10'],
	['tenant-a-egress', 'tenant-a', '203.0.113.11'],
	['payments-egress', 'payments', '203.0.113.12'],
])
	policies.push({
		name,
		kind: 'egressip',
		backing: 'EgressIP',
		target: `namespace ${ns}`,
		namespaces: [ns],
		rules: [{ direction: 'Egress', action: 'SNAT', peer: ip }],
		sourceFile: `outbound/${name}.yaml`,
		sync: 'Synced',
		health: 'Healthy',
	});
policies.push({
	name: 'backup-via-dc',
	kind: 'route',
	backing: 'AdminPolicyBasedExternalRoute',
	target: 'namespace backup-ops',
	namespaces: ['backup-ops'],
	rules: [{ direction: 'Egress', action: 'Route', peer: 'via 192.0.2.1' }],
	sourceFile: 'outbound/backup-via-dc.yaml',
	sync: 'Synced',
	health: 'Healthy',
});

// ── run state, placement, addresses ───────────────────────────────────────────

const STOPPED = new Set(['web-prod/web-2', 'web-prod/web-6']);
const PINNED_HOST = {
	'web-prod/web-1': 'host-07',
	'db-prod/db-1': 'host-07',
	'web-prod/web-3': 'host-12',
	'web-prod/web-4': 'host-12',
	'web-prod/web-5': 'host-21',
	'web-prod/web-14': 'host-12',
	'web-prod/web-8': 'host-07',
};
const ipOn = (prefix, i) => `${prefix}.${Math.floor((i + 10) / 256)}.${(i + 10) % 256}`;
let podSeq = 0;
const webIndex = new Map(
	inNs('web-prod')
		.slice(0, 389)
		.map((v, i) => [v, i]),
);
const taIndex = new Map(inNs('tenant-a').map((v, i) => [v, i]));

const SIZES = [
	[2, '4Gi'],
	[2, '4Gi'],
	[2, '4Gi'],
	[4, '8Gi'],
	[2, '8Gi'],
	[8, '16Gi'],
];
const modelVMs = new Map();
for (const v of vms) {
	const key = `${v.namespace}/${v.name}`;
	const running = !STOPPED.has(key) && (key in PINNED_HOST || rand() >= 0.06);
	const [cpu, memory] =
		v.namespace === 'db-prod'
			? [v.name === 'db-1' ? 2 : 4, '8Gi']
			: SIZES[int(0, SIZES.length - 1)];
	const image = v.slot?.image ?? 'fedora-coreos';
	const disks = v.slot
		? [
				{
					name: 'rootdisk',
					type: 'dataVolume',
					size: diskSize(image),
					storageClass: v.slot.cls,
					image: `${IMAGE_NS}/${image}`,
					claim: `${v.name}-rootdisk`,
				},
				...(v.slot.disks === 2
					? [
							{
								name: 'data',
								type: 'dataVolume',
								size: '100Gi',
								storageClass: v.slot.cls,
								image: `${IMAGE_NS}/${image}`,
								claim: `${v.name}-data`,
							},
						]
					: []),
			]
		: [
				{ name: 'containerdisk', type: 'containerDisk' },
				{ name: 'cloudinitdisk', type: 'cloudInitNoCloud' },
			];
	let guestIP;
	if (running) {
		if (webIndex.has(v)) guestIP = ipOn('10.40', webIndex.get(v));
		else if (taIndex.has(v)) guestIP = ipOn('10.150', taIndex.get(v));
		else if (key === 'db-prod/db-1') guestIP = '10.128.0.12';
		else guestIP = ipOn('10.128', 3 + podSeq++);
	}
	const nics = [
		{ name: 'default', network: 'pod' },
		...v.nics.map((ref) => ({ name: ref.split('/').at(-1), network: ref })),
	];
	const appLabel =
		v.namespace === 'web-prod' ? 'web' : v.namespace === 'db-prod' ? 'db' : v.namespace;
	modelVMs.set(v, {
		namespace: v.namespace,
		name: v.name,
		power: running ? 'On' : 'Off',
		cpuCores: cpu,
		memory,
		labels: { app: appLabel },
		disks,
		networks: nics,
		sourceFile: `vms/${v.name}.yaml`,
		phase: running ? 'Running' : 'Stopped',
		...(running
			? {
					guestIP,
					ips: [guestIP],
					nodeName: PINNED_HOST[key] ?? SCHEDULABLE[int(0, SCHEDULABLE.length - 1)],
					os: osOf(image),
					vcpus: cpu,
					startedAt: iso(int(60, 60 * DAY)),
				}
			: {}),
		sync: 'Synced',
		health: 'Healthy',
	});
}

const all = [...modelVMs.values()];

// ── storage listings: every snapshot and disk claim ────────────────────────────

const persistentDisks = (v) => v.disks.filter((d) => d.type === 'dataVolume');
const storageSnapshots = [];
for (const v of all) {
	const key = `${v.namespace}/${v.name}`;
	const plan = snapshotPlan[key];
	if (!plan) continue;
	const named = key === 'web-prod/web-1' ? WEB1_SNAPSHOTS : null;
	for (let i = 0; i < plan[0]; i++) {
		const [name, minsAgo] = named ? named[i] : [`${v.name}-snap-${plan[0] - i}`, plan[1] + i * DAY];
		storageSnapshots.push({
			namespace: v.namespace,
			vm: v.name,
			name,
			created: iso(minsAgo),
			phase: 'Succeeded',
			readyToUse: true,
			indications: ['Online', 'GuestAgent'],
			volumes: persistentDisks(v).map((d) => d.name),
		});
	}
}
storageSnapshots.sort((a, b) => b.created.localeCompare(a.created));
check('snapshots listed', storageSnapshots.length, 9870);

// A stopped VM's local disk stays on the host it was bound to.
const STOPPED_HOST = { 'web-prod/web-2': 'host-07', 'web-prod/web-6': 'host-17' };
const storageClaims = all.flatMap((v) =>
	persistentDisks(v).map((d) => {
		const local = d.storageClass !== 'bulk-hdd';
		const node =
			v.nodeName ?? STOPPED_HOST[`${v.namespace}/${v.name}`] ?? HOSTS[v.name.length % HOSTS.length];
		return {
			namespace: v.namespace,
			name: d.claim,
			storageClass: d.storageClass,
			capacity: d.size,
			phase: 'Bound',
			...(local ? { node } : {}),
		};
	}),
);
const runningVMs = all.filter((v) => v.phase === 'Running');
const memBytes = (m) => Number(m.replace('Gi', '')) * GiB;

// ── hosts: load and commitment, from where the running VMs sit ────────────────

const perHost = new Map(HOSTS.map((h) => [h, { vcpu: 0, mem: 0, vms: 0 }]));
for (const v of runningVMs) {
	const h = perHost.get(v.nodeName);
	h.vcpu += v.cpuCores;
	h.mem += memBytes(v.memory);
	h.vms++;
}
const hostCapacity = {
	updated: Math.floor(Date.now() / 1000),
	nodes: HOSTS.map((h) => ({
		node: h,
		cpuAllocatable: CORES,
		vcpuAllocated: perHost.get(h).vcpu,
		memAllocatable: HOST_MEM,
		memAllocated: perHost.get(h).mem,
	})),
};
const loadNodes = HOSTS.map((h) => {
	const x = perHost.get(h);
	return {
		node: h,
		pct: h === MAINTENANCE ? 2 : Math.round(((x.vcpu / CORES) * 0.24 + rand() * 0.08) * 1000) / 10,
		mem: h === MAINTENANCE ? 4 : Math.round((x.mem / HOST_MEM) * 0.85 * 1000) / 10,
		...(h === MAINTENANCE ? { unschedulable: true } : {}),
	};
}).sort((a, b) => b.pct - a.pct);
const hostLoad = {
	updated: Math.floor(Date.now() / 1000),
	workers: 48,
	mean: Math.round((sum(loadNodes.map((n) => n.pct)) / 48) * 10) / 10,
	nodes: loadNodes,
};

const vcpuAllocated = sum(runningVMs.map((v) => v.cpuCores));
const memAllocated = sum(runningVMs.map((v) => memBytes(v.memory)));
const clusterSummary = {
	updated: Math.floor(Date.now() / 1000),
	cpu: {
		used: Math.round(vcpuAllocated * 0.31),
		allocated: vcpuAllocated,
		total: CORES * 48,
		spark: [0.29, 0.3, 0.31, 0.32, 0.31].map((f) => Math.round(vcpuAllocated * f)),
	},
	memory: {
		used: Math.round(memAllocated * 0.72),
		allocated: memAllocated,
		total: HOST_MEM * 48,
		spark: [0.7, 0.71, 0.72, 0.72, 0.72].map((f) => Math.round(memAllocated * f)),
	},
	storage: { used: (760 - 412 + (96 - 58)) * TiB, total: (760 + 96) * TiB },
	vms: { running: runningVMs.length, stopped: all.length - runningVMs.length },
	topCpu: [
		['web-prod', 'web-1', 1.82],
		['batch-prod', 'batch-3', 1.75],
		['analytics', 'an-12', 1.6],
		['db-prod', 'db-1', 1.41],
		['web-prod', 'web-14', 1.4],
	].map(([namespace, name, value]) => ({ namespace, name, value })),
	topMemory: [
		['db-prod', 'db-1', 7.4 * GiB],
		['analytics', 'an-12', 7.1 * GiB],
		['batch-prod', 'batch-3', 6.8 * GiB],
		['web-prod', 'web-1', 3.6 * GiB],
		['web-prod', 'web-14', 3.5 * GiB],
	].map(([namespace, name, value]) => ({ namespace, name, value })),
};

// ── storage classes and options ────────────────────────────────────────────────

const perHostFree = (totalTiB, hosts) =>
	apportion(
		hosts.map(() => int(80, 120)),
		totalTiB * 1024,
	).map((gib, i) => ({ topology: hosts[i], free: gib * GiB }));
const storageClasses = [
	{
		name: 'lvms-vgslow',
		default: true,
		description: 'Local disks',
		provisioner: 'topolvm.io',
		reclaimPolicy: 'Delete',
		bindingMode: 'WaitForFirstConsumer',
		expandable: true,
		created: iso(300 * DAY),
		snapshotClass: 'lvms-vgslow',
		profile: {
			accessModes: ['ReadWriteOnce'],
			volumeMode: 'Block',
			cloneStrategy: 'snapshot',
			recognized: true,
		},
		free: 412 * TiB,
		segments: perHostFree(412, HOSTS),
	},
	{
		name: 'lvms-vgfast',
		description: 'Local NVMe',
		provisioner: 'topolvm.io',
		reclaimPolicy: 'Delete',
		bindingMode: 'WaitForFirstConsumer',
		expandable: true,
		created: iso(300 * DAY),
		snapshotClass: 'lvms-vgfast',
		profile: {
			accessModes: ['ReadWriteOnce'],
			volumeMode: 'Block',
			cloneStrategy: 'snapshot',
			recognized: true,
		},
		free: 58 * TiB,
		segments: perHostFree(58, HOSTS.slice(16, 40)),
	},
	{
		name: 'bulk-hdd',
		description: 'Shared NFS',
		provisioner: 'nfs.csi.k8s.io',
		reclaimPolicy: 'Delete',
		bindingMode: 'Immediate',
		expandable: true,
		created: iso(300 * DAY),
		snapshotClass: 'bulk-hdd-snap',
		profile: {
			accessModes: ['ReadWriteMany'],
			volumeMode: 'Filesystem',
			shared: true,
			cloneStrategy: 'copy',
			recognized: true,
		},
	},
];

const options = {
	instancetypes: [
		{ name: 'u1.small', cpu: 1, memory: '2Gi' },
		{ name: 'u1.medium', cpu: 2, memory: '4Gi' },
		{ name: 'u1.large', cpu: 4, memory: '8Gi' },
		{ name: 'u1.xlarge', cpu: 8, memory: '16Gi' },
	],
	preferences: [
		{ name: 'fedora' },
		{ name: 'centos.stream9' },
		{ name: 'rhel.9' },
		{ name: 'windows.2k25', minCPU: 2, minMemory: '4Gi' },
	],
	osImages: IMAGES.map((i) => ({ name: i.name, namespace: IMAGE_NS, ready: true })),
	networks: [],
	storageClasses: storageClasses.map((c) => ({
		name: c.name,
		...(c.default ? { default: true } : {}),
	})),
};

// ── the inventory tree ─────────────────────────────────────────────────────────

const projects = PROJECTS.map((p) => ({
	name: p.name,
	repo: `https://forge.example/dotvirt/${p.name}.git`,
	gitOps: { sync: 'Synced', health: 'Healthy', operation: 'Succeeded', revision: 'a1b2c3d' },
	namespaces: p.namespaces.map((ns) => ({
		namespace: ns.namespace,
		canAuthor: true,
		vms: ns.names.map((n) => modelVMs.get(get(ns.namespace, n))),
	})),
}));

/** @param {any} base the base scenario, for the planes the scale scenario leaves as they are */
export function scaleScenario(base) {
	return {
		...base,
		name: 'scale',
		inventory: {
			projects,
			proposals: [],
			// The platform tier's one standing problem: the dc-vlan uplink policy
			// fails on host-41, so dc-vlan-net is cut off there.
			platform: {
				sync: 'Synced',
				health: 'Degraded',
				operation: 'Succeeded',
				revision: 'c0ffee1',
				unhealthy: [
					{
						kind: 'NodeNetworkConfigurationPolicy',
						name: 'dc-vlan-bridge',
						health: 'Degraded',
						message: 'FailedToConfigure: 1/6 nodes failed to configure (host-41)',
					},
				],
			},
			networksVersion: 1,
			tasksVersion: 1,
		},
		tasks: [
			{
				kind: 'merge',
				verb: 'Merged',
				project: 'team-web',
				prNumber: 42,
				prURL: 'https://forge.example/dotvirt/team-web/pulls/42',
				title: 'Attach web-388 and web-389',
				by: 'admin',
				ok: true,
				at: iso(3 * DAY),
			},
			{
				kind: 'op',
				verb: 'Snapshot',
				namespace: 'web-prod',
				name: 'web-1',
				project: 'team-web',
				by: 'admin',
				ok: true,
				at: iso(120),
			},
			{
				kind: 'op',
				verb: 'Enter maintenance',
				name: MAINTENANCE,
				by: 'admin',
				ok: true,
				at: iso(600),
			},
		],
		alarms: [],
		events: [
			{
				namespace: 'web-prod',
				name: 'web-1',
				type: 'Normal',
				reason: 'SnapshotSucceeded',
				message: 'VirtualMachineSnapshot before-release-41 is ready',
				object: 'VirtualMachine',
				lastSeen: iso(120),
			},
			{
				namespace: 'demo',
				name: 'vm-a',
				type: 'Warning',
				reason: 'NetworkUnavailable',
				message: 'dc-vlan-net is not configured on host-41',
				count: 3,
				object: 'VirtualMachineInstance',
				lastSeen: iso(30),
			},
		],
		options,
		storageClasses,
		networks: {
			networks,
			uplinks,
			physicalAdapters,
			nmstatePresent: true,
			canManage: true,
			caps: base.networks.caps,
		},
		policies: { policies },
		nodes: HOSTS.map((h) =>
			h === MAINTENANCE
				? { name: h, ready: true, unschedulable: true, maintenance: true }
				: { name: h, ready: true },
		),
		quotas: [],
		drafts: {
			'team-web': {
				base: 'main',
				branch: 'dotvirt/proposed/admin/team-web',
				count: 1,
				items: [
					{
						kind: 'edit',
						namespace: 'web-prod',
						name: 'web-1',
						changes: [
							{
								field: 'Disk rootdisk storage',
								action: 'change',
								from: 'lvms-vgslow',
								to: 'bulk-hdd',
							},
						],
						edit: { migrateVolumes: [{ name: 'rootdisk', storageClass: 'bulk-hdd' }] },
					},
				],
			},
		},
		storageSnapshots,
		storageClaims,
		metrics: {
			...base.metrics,
			clusterSummary,
			hostLoad,
			hostCapacity,
		},
	};
}

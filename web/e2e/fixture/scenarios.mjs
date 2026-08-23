// Scenario states for the fixture API server. Each scenario is a full backend
// state the UI can be pointed at — including the corner-case states a live
// cluster is almost never in (standing sync failures, degraded planes, huge
// fleets). Shapes mirror src/lib/model.gen.ts; the UI is the contract's judge.

const now = () => new Date();
const iso = (minsAgo) => new Date(now().getTime() - minsAgo * 60_000).toISOString();

/** @returns {import('../../src/lib/model.gen').VM} */
function vm(namespace, name, over = {}) {
	return {
		namespace,
		name,
		power: 'On',
		cpuCores: 2,
		memory: '4Gi',
		labels: { app: name },
		disks: [{ name: 'rootdisk', type: 'dataVolume', size: '20Gi' }],
		networks: [{ name: 'default', network: 'pod' }],
		sourceFile: `vms/${name}.yaml`,
		phase: 'Running',
		guestIP: '10.128.0.10',
		ips: ['10.128.0.10'],
		nodeName: 'worker-1',
		os: 'Fedora Linux 42',
		vcpus: 2,
		startedAt: iso(240),
		sync: 'Synced',
		health: 'Healthy',
		...over,
	};
}

const options = {
	instancetypes: [
		{ name: 'u1.small', cpu: 1, memory: '2Gi' },
		{ name: 'u1.medium', cpu: 2, memory: '4Gi' },
		{ name: 'u1.large', cpu: 4, memory: '8Gi' },
	],
	preferences: [{ name: 'fedora' }, { name: 'windows.11', minCPU: 2, minMemory: '4Gi' }],
	osImages: [
		{ name: 'fedora-42', namespace: 'dotvirt-images', ready: true },
		{ name: 'debian-13', namespace: 'dotvirt-images', ready: false },
	],
	networks: [{ name: 'lan-101', namespace: 'web-prod' }],
	storageClasses: [{ name: 'fast-ssd', default: true }, { name: 'bulk-hdd' }],
};

const networks = {
	networks: [
		{
			name: 'web-net',
			kind: 'default',
			scope: 'project',
			namespace: 'web-prod',
			subnets: ['192.168.10.0/24'],
			attachRef: 'web-prod/web-net',
			backing: 'UserDefinedNetwork',
			topology: 'Layer2',
			sync: 'Synced',
		},
		{
			name: 'lan-101',
			kind: 'vlan',
			scope: 'shared',
			vlan: 101,
			uplink: 'physnet',
			attachRef: 'lan-101',
			backing: 'ClusterUserDefinedNetwork',
			topology: 'Localnet',
			namespaces: ['web-prod', 'db-prod'],
			sync: 'Synced',
		},
	],
	uplinks: [
		{ name: 'default', bridge: 'br-ex', builtin: true, nodeCount: 3 },
		{
			name: 'physnet',
			bridge: 'br-physnet',
			nodes: ['worker-1', 'worker-2'],
			nodeCount: 2,
			ports: ['eno2'],
			status: 'Available',
		},
	],
	physicalAdapters: [
		{
			name: 'eno1',
			node: 'worker-1',
			type: 'ethernet',
			state: 'up',
			mtu: 1500,
			role: 'cluster-uplink',
		},
		{ name: 'eno2', node: 'worker-1', type: 'ethernet', state: 'up', mtu: 9000, role: 'enslaved' },
	],
	nmstatePresent: true,
	canManage: true,
	caps: {
		sharedSegment: true,
		uplink: true,
		namespace: true,
		egressIP: true,
		externalRoute: true,
		adminNetworkPolicy: true,
	},
};

const policies = {
	policies: [
		{
			name: 'allow-web-to-db',
			kind: 'dfw',
			namespace: 'db-prod',
			backing: 'NetworkPolicy',
			target: 'app=db-1',
			rules: [{ direction: 'Ingress', action: 'Allow', peer: 'app=web-1', ports: 'TCP/5432' }],
			sync: 'Synced',
		},
		{
			name: 'deny-lateral',
			kind: 'admin',
			backing: 'AdminNetworkPolicy',
			priority: 10,
			target: 'all namespaces',
			rules: [{ direction: 'Ingress', action: 'Deny', peer: 'any', ports: '' }],
			sync: 'Synced',
		},
	],
};

const nodes = [
	{ name: 'worker-1', ready: true },
	{ name: 'worker-2', ready: true },
	{ name: 'worker-3', ready: true, unschedulable: true, maintenance: true },
];

function clusterSummary(vmCount) {
	return {
		updated: Math.floor(now().getTime() / 1000),
		cpu: { used: 9.5, allocated: vmCount * 2, total: 48, spark: [8, 9, 9.5, 10, 9.5] },
		memory: {
			used: 34e9,
			allocated: vmCount * 4e9,
			total: 192e9,
			spark: [30e9, 32e9, 34e9, 33e9, 34e9],
		},
		// Zero-of-zero on an empty scope: the ring must read "no data".
		storage: vmCount ? { used: 210e9, total: 2e12 } : { used: 0, total: 0 },
		// Lowercase keys, like the kubevirt_vmi_info-derived map; stopped is the
		// backend's snapshot overlay (VM objects without an instance).
		vms: { running: Math.max(vmCount - 1, 0), stopped: Math.min(vmCount, 1) },
		topCpu: [{ namespace: 'web-prod', name: 'web-1', value: 38.2 }],
		topMemory: [{ namespace: 'db-prod', name: 'db-1', value: 3.1e9 }],
	};
}

const hostLoad = {
	updated: Math.floor(now().getTime() / 1000),
	workers: 3,
	mean: 31,
	nodes: [
		{ node: 'worker-1', pct: 52, mem: 61 },
		{ node: 'worker-2', pct: 28, mem: 40 },
		{ node: 'worker-3', pct: 13, mem: 22, unschedulable: true },
	],
};

const hostCapacity = {
	updated: Math.floor(now().getTime() / 1000),
	nodes: [
		{
			node: 'worker-1',
			cpuAllocatable: 16,
			vcpuAllocated: 10,
			memAllocatable: 64e9,
			memAllocated: 24e9,
		},
		{
			node: 'worker-2',
			cpuAllocatable: 16,
			vcpuAllocated: 6,
			memAllocatable: 64e9,
			memAllocated: 12e9,
		},
		{
			node: 'worker-3',
			cpuAllocatable: 16,
			vcpuAllocated: 2,
			memAllocatable: 64e9,
			memAllocated: 6e9,
		},
	],
};

function vmMetrics(range = '1h') {
	const times = Array.from(
		{ length: 30 },
		(_, i) => Math.floor(now().getTime() / 1000) - (29 - i) * 120,
	);
	const wave = (base, amp) => times.map((_, i) => base + amp * Math.sin(i / 4));
	return {
		range,
		stepSec: 120,
		charts: [
			{
				key: 'cpu',
				title: 'CPU usage',
				unit: '%',
				times,
				series: [{ name: 'usage', values: wave(35, 12) }],
			},
			{
				key: 'mem',
				title: 'Memory',
				unit: 'bytes',
				stacked: true,
				times,
				series: [
					{ name: 'used', values: wave(2.1e9, 4e8) },
					{ name: 'cached', values: wave(0.9e9, 2e8) },
				],
			},
			{
				key: 'net',
				title: 'Network throughput',
				unit: 'Bps',
				times,
				series: [
					{ name: 'rx', values: wave(3e6, 2e6) },
					{ name: 'tx', values: wave(1e6, 6e5) },
				],
			},
		],
	};
}

const vmUsage = {
	updated: Math.floor(now().getTime() / 1000),
	cpu: { used: 34, total: 100, spark: [28, 31, 34, 30, 34] },
	memory: { used: 2.4e9, total: 4.29e9, spark: [2.1e9, 2.3e9, 2.4e9] },
	storage: { used: 8.2e9, total: 21.5e9 },
};

// ── base: a small healthy fleet ────────────────────────────────────────────────

const base = {
	name: 'base',
	user: { username: 'admin', groups: ['dotvirt-admins'] },
	inventory: {
		projects: [
			{
				name: 'team-web',
				repo: 'https://forge.example/dotvirt/team-web.git',
				gitOps: { sync: 'Synced', health: 'Healthy', operation: 'Succeeded', revision: 'ab12cd3' },
				namespaces: [
					{
						namespace: 'web-prod',
						vms: [
							vm('web-prod', 'web-1'),
							vm('web-prod', 'web-2', {
								power: 'Off',
								phase: 'Stopped',
								guestIP: undefined,
								ips: undefined,
								nodeName: undefined,
								startedAt: undefined,
								os: undefined,
							}),
						],
					},
				],
			},
			{
				name: 'team-db',
				repo: 'https://forge.example/dotvirt/team-db.git',
				gitOps: { sync: 'Synced', health: 'Healthy', operation: 'Succeeded', revision: '99fe210' },
				namespaces: [
					{
						namespace: 'db-prod',
						vms: [vm('db-prod', 'db-1', { instancetype: 'u1.large', vcpus: 4, memory: '8Gi' })],
					},
				],
			},
			{
				name: 'team-batch',
				repo: 'https://forge.example/dotvirt/team-batch.git',
				// Converged after a failed apply: operation history says Failed, but
				// sync is green - the UI must treat the record as history (#154).
				gitOps: {
					sync: 'Synced',
					health: 'Healthy',
					operation: 'Failed',
					syncError: 'one earlier apply was refused; since converged',
					revision: 'cc0011d',
				},
				namespaces: [
					{
						namespace: 'batch-prod',
						vms: [
							vm('batch-prod', 'batch-1', {
								nodeName: 'worker-2',
								guestIP: '10.128.0.30',
								ips: ['10.128.0.30'],
							}),
						],
					},
				],
			},
		],
		proposals: [
			{
				project: 'team-db',
				prNumber: 41,
				prURL: 'https://forge.example/dotvirt/team-db/pulls/41',
				title: 'db-1: raise memory to 12Gi',
			},
		],
		networksVersion: 1,
		tasksVersion: 1,
	},
	tasks: [
		{
			kind: 'op',
			verb: 'Restart',
			namespace: 'web-prod',
			name: 'web-1',
			project: 'team-web',
			by: 'admin',
			ok: true,
			at: iso(12),
		},
		{
			kind: 'merge',
			verb: 'Merged',
			project: 'team-web',
			prNumber: 40,
			prURL: 'https://forge.example/dotvirt/team-web/pulls/40',
			title: 'web-2: add data disk',
			by: 'admin',
			ok: true,
			at: iso(95),
		},
	],
	alarms: [
		{
			name: 'KubeVirtVMIExcessiveMigrations',
			severity: 'warning',
			namespace: 'web-prod',
			vm: 'web-1',
			count: 1,
		},
	],
	events: [
		{
			namespace: 'web-prod',
			name: 'web-1',
			type: 'Normal',
			reason: 'Started',
			message: 'VirtualMachineInstance started',
			object: 'VirtualMachineInstance',
			lastSeen: iso(240),
		},
		{
			namespace: 'web-prod',
			name: 'web-2',
			type: 'Warning',
			reason: 'FailedScheduling',
			message: '0/3 nodes carry enough free memory',
			count: 4,
			object: 'VirtualMachineInstance',
			lastSeen: iso(30),
		},
	],
	options,
	networks,
	policies,
	nodes,
	quotas: [
		{
			namespace: 'web-prod',
			name: 'compute',
			items: [
				{ resource: 'requests.cpu', used: 4, hard: 16, unit: 'cores' },
				{ resource: 'requests.memory', used: 8e9, hard: 64e9, unit: 'bytes' },
			],
		},
	],
	drs: {
		configured: false,
		psiConfigured: false,
		live: { apiPresent: false, synced: true, deployed: false, available: false },
		canManage: true,
		canPSI: true,
	},
	templates: { templates: [] },
	permissions: {
		namespace: 'web-prod',
		capabilities: [
			{ id: 'view', label: 'View', allowed: true },
			{ id: 'console', label: 'Open console', allowed: true },
			{ id: 'restart', label: 'Restart', allowed: true },
		],
	},
	drafts: {},
	metrics: {
		clusterSummary: clusterSummary(3),
		hostLoad,
		hostCapacity,
		vmMetrics: vmMetrics(),
		vmUsage,
	},
};

// ── empty: pristine install, nothing visible yet ───────────────────────────────

const empty = {
	...base,
	name: 'empty',
	inventory: { projects: [], networksVersion: 1, tasksVersion: 1 },
	tasks: [],
	alarms: [],
	events: [],
	quotas: [],
	metrics: { ...base.metrics, clusterSummary: clusterSummary(0) },
};

// ── degraded: forge unreachable, live plane down, optional planes absent ───────
// Drawn from real incidents: a flapping forge (503s on every clone), a stale SA
// token (live reads fail while git still renders), metrics/alerts not deployed.

const degraded = {
	...base,
	name: 'degraded',
	inventory: {
		projects: [
			{
				name: 'team-web',
				// A project whose namespaces are labeled but whose repo cannot be
				// read: the inventory carries the error instead of dropping the rows.
				error: 'clone https://forge.example/dotvirt/team-web.git: 503 Service Unavailable',
				namespaces: [],
			},
			{
				name: 'team-db',
				repo: 'https://forge.example/dotvirt/team-db.git',
				gitOps: {
					sync: 'Unknown',
					health: 'Unknown',
					syncError: 'comparison error: repository not accessible',
				},
				namespaces: [
					{
						namespace: 'db-prod',
						// Git-only rows: the live plane is down, so no phase/IP/node.
						vms: [
							vm('db-prod', 'db-1', {
								phase: undefined,
								guestIP: undefined,
								ips: undefined,
								nodeName: undefined,
								os: undefined,
								startedAt: undefined,
								vcpus: undefined,
								sync: 'Unknown',
								health: undefined,
							}),
						],
					},
				],
			},
		],
		warnings: ['live VM state unavailable: the cluster watch is failing; showing git state only'],
		networksVersion: 1,
		tasksVersion: 1,
	},
	tasks: [],
	events: [],
	networks: {
		...networks,
		nmstatePresent: false,
		uplinks: [],
		physicalAdapters: [],
		canManage: false,
		caps: {
			sharedSegment: false,
			uplink: false,
			namespace: true,
			egressIP: false,
			externalRoute: false,
			adminNetworkPolicy: false,
		},
	},
	// The optional planes answer 503 (down), not empty (fine): the UI must
	// degrade with a visible note, never a blank pane or a crash.
	alarms: null,
	metrics: null,
};

// ── drift: every GitOps trouble state at once ──────────────────────────────────
// Standing sync failure (stays after the operation ends), Pending (declared,
// awaiting first sync), NotTracked (cluster-only, adoptable), a prune warning
// on the draft, and an adoptable legacy namespace.

const drift = {
	...base,
	name: 'drift',
	inventory: {
		projects: [
			{
				name: 'team-web',
				repo: 'https://forge.example/dotvirt/team-web.git',
				gitOps: {
					sync: 'OutOfSync',
					health: 'Degraded',
					operation: 'Failed',
					syncError:
						'admission webhook "virt-api.kubevirt.io" denied the request: spec.template.spec.domain.memory: requests must not exceed limits',
					revision: 'ab12cd3',
				},
				namespaces: [
					{
						namespace: 'web-prod',
						vms: [
							vm('web-prod', 'web-1'),
							// The standing failure: OutOfSync WITH an apply error. The tree
							// badge and issues plane must surface it; a plain OutOfSync
							// (mid-sync) must not.
							vm('web-prod', 'web-3', {
								sync: 'OutOfSync',
								health: 'Degraded',
								syncError: 'admission webhook "virt-api.kubevirt.io" denied the request',
							}),
						],
					},
				],
			},
			{
				name: 'team-db',
				repo: 'https://forge.example/dotvirt/team-db.git',
				gitOps: { sync: 'Synced', health: 'Healthy', operation: 'Succeeded', revision: '99fe210' },
				namespaces: [
					{
						namespace: 'db-prod',
						vms: [
							vm('db-prod', 'db-1'),
							vm('db-prod', 'pinned-legacy', {
								scheduling: { custom: true },
								nodeName: 'worker-2',
							}),
							// Cluster-only in a HEALTHY project: the adopt banner must offer
							// the capture (a broken repo suppresses it; RepoBanner owns those).
							vm('db-prod', 'legacy-1', {
								sync: 'NotTracked',
								health: undefined,
								sourceFile: '',
							}),
							// Declared in git, no Application reports it yet: Pending, which
							// must NOT read as adoption failure or NotTracked.
							vm('db-prod', 'db-2', {
								sync: 'Pending',
								phase: undefined,
								guestIP: undefined,
								ips: undefined,
								nodeName: undefined,
								os: undefined,
								startedAt: undefined,
								health: undefined,
							}),
						],
					},
				],
			},
		],
		adoptable: [{ namespace: 'legacy-ns', vms: 2 }],
		proposals: [],
		networksVersion: 2,
		tasksVersion: 2,
	},
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
					changes: [{ field: 'memory', action: 'change', from: '4Gi', to: '8Gi' }],
				},
			],
			warning:
				'Merging will prune 2 objects the repo no longer declares (web-prod/old-vm, web-prod/old-net). Verify they are meant to go.',
		},
	},
	vmDrift: {
		'web-prod/web-1': {
			drift: true,
			changes: [{ field: 'cpuCores', action: 'change', from: '2', to: '4' }],
		},
	},
};

// ── large: a fleet big enough to expose rendering and update-storm issues ─────

function largeScenario() {
	const projects = [];
	for (let p = 0; p < 12; p++) {
		const nss = [];
		for (let n = 0; n < 2; n++) {
			const namespace = `tenant-${p}-ns-${n}`;
			const vms = [];
			for (let v = 0; v < 13; v++) {
				vms.push(
					vm(namespace, `vm-${p}-${n}-${v}`, {
						power: v % 5 === 0 ? 'Off' : 'On',
						phase: v % 5 === 0 ? 'Stopped' : 'Running',
						nodeName: `worker-${(v % 3) + 1}`,
						guestIP: `10.${p}.${n}.${v + 10}`,
						ips: [`10.${p}.${n}.${v + 10}`],
					}),
				);
			}
			nss.push({ namespace, vms });
		}
		projects.push({
			name: `tenant-${p}`,
			repo: `https://forge.example/dotvirt/tenant-${p}.git`,
			gitOps: { sync: 'Synced', health: 'Healthy', operation: 'Succeeded', revision: 'aaaa111' },
			namespaces: nss,
		});
	}
	return {
		...base,
		name: 'large',
		inventory: { projects, networksVersion: 1, tasksVersion: 1 },
		metrics: { ...base.metrics, clusterSummary: clusterSummary(312) },
	};
}

// SSO login-screen states: pending guides an admin to finish setup instead of
// offering a button that fails; ready offers the SSO path beside the token.
const sso = { ...base, name: 'sso', authMethods: { sso: true, ssoPending: true } };
const ssoready = { ...base, name: 'ssoready', authMethods: { sso: true, ssoPending: false } };

export const scenarios = {
	sso,
	ssoready,
	base,
	empty,
	degraded,
	drift,
	large: largeScenario(),
};

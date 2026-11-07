import { createNetworks, createTemplates } from './create.mjs';

// Scenario states for the fixture API server. Each scenario is a full backend
// state the UI can be pointed at — including the corner-case states a live
// cluster is almost never in (standing sync failures, degraded planes, huge
// fleets). Shapes mirror src/lib/model.gen.ts; the UI is the contract's judge.

import { scaleScenario } from './scale.mjs';

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

// The Storage section's fact sheet: one local (per-node capacity, RWO) and
// one shared class, so the summary shows both live-migration verdicts.
const storageClasses = [
	{
		name: 'bulk-hdd',
		provisioner: 'nfs.csi.k8s.io',
		reclaimPolicy: 'Delete',
		bindingMode: 'Immediate',
		expandable: true,
		created: iso(200 * 1440),
		snapshotClass: 'bulk-hdd-snap',
		profile: {
			accessModes: ['ReadWriteMany'],
			volumeMode: 'Filesystem',
			shared: true,
			cloneStrategy: 'copy',
			recognized: true,
		},
	},
	{
		name: 'fast-ssd',
		default: true,
		description: 'NVMe pool',
		provisioner: 'topolvm.io',
		reclaimPolicy: 'Delete',
		bindingMode: 'WaitForFirstConsumer',
		expandable: true,
		created: iso(200 * 1440),
		snapshotClass: 'fast-ssd',
		profile: {
			accessModes: ['ReadWriteOnce'],
			volumeMode: 'Block',
			cloneStrategy: 'snapshot',
			recognized: true,
		},
		free: 3.2e12,
		segments: [
			{ topology: 'worker-1', free: 1.1e12 },
			{ topology: 'worker-2', free: 2.1e12 },
		],
	},
];

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
		uplink: true,
		namespace: true,
		egressIP: true,
		externalRoute: true,
		adminNetworkPolicy: true,
		platform: true,
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
		topCpu: [{ namespace: 'web-prod', name: 'web-1', value: 0.76 }], // cores in use
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

// ── networking: the Networking section's catalog, rules and history ──────────
// One picture of the networking states the Map, the network page and the
// firewall show: project networks, shared overlays, VLAN networks, rules on
// both sides of a network, a range reused by isolated networks; drift adds an
// uplink failing on one host, scale an overlap that is a problem. Rule texts
// are what the server renders from the typed rules.

const netCatalog = {
	...networks,
	networks: [
		{
			name: 'web-net',
			kind: 'default',
			scope: 'project',
			namespace: 'web-prod',
			subnets: ['10.40.0.0/16'],
			attachRef: 'web-prod/web-net',
			backing: 'UserDefinedNetwork',
			topology: 'Layer3',
			sourceFile: 'networks/web-net.yaml',
			sync: 'Synced',
		},
		{
			name: 'internal-l2',
			kind: 'internal',
			scope: 'project',
			namespace: 'batch-prod',
			subnets: ['10.20.9.0/24'],
			attachRef: 'batch-prod/internal-l2',
			backing: 'UserDefinedNetwork',
			topology: 'Layer2',
			sourceFile: 'networks/internal-l2.yaml',
			sync: 'Synced',
		},
		{
			name: 'blue-secondary',
			kind: 'internal',
			scope: 'shared',
			subnets: ['10.222.1.0/24'],
			attachRef: 'blue-secondary',
			backing: 'ClusterUserDefinedNetwork',
			topology: 'Layer2',
			namespaces: ['web-prod', 'db-prod', 'batch-prod'],
			sourceFile: 'networks/blue-secondary.yaml',
			sync: 'Synced',
		},
		{
			name: 'green-secondary',
			kind: 'internal',
			scope: 'shared',
			subnets: ['10.222.2.0/24'],
			attachRef: 'green-secondary',
			backing: 'ClusterUserDefinedNetwork',
			topology: 'Layer2',
			namespaces: ['web-prod'],
			sourceFile: 'networks/green-secondary.yaml',
			sync: 'Synced',
		},
		{ ...networks.networks[1], sourceFile: 'networks/lan-101.yaml' },
		// The create sheet's networks: an overlapping pair in two namespaces
		// (isolated, so never an issue) and one with no range.
		...createNetworks,
		{
			name: 'dc-vlan-net',
			kind: 'vlan',
			scope: 'shared',
			vlan: 100,
			uplink: 'dc-vlan',
			attachRef: 'dc-vlan-net',
			backing: 'ClusterUserDefinedNetwork',
			topology: 'Localnet',
			namespaces: ['batch-prod', 'db-prod'],
			sourceFile: 'networks/dc-vlan-net.yaml',
			sync: 'Synced',
		},
	],
	uplinks: [
		{
			name: 'default',
			bridge: 'br-ex',
			builtin: true,
			nodes: ['worker-1', 'worker-2', 'worker-3'],
			nodeCount: 3,
		},
		{
			name: 'physnet',
			bridge: 'br-physnet',
			nodes: ['worker-1', 'worker-2'],
			nodeCount: 2,
			policy: 'uplink-physnet',
			sourceFile: 'uplinks/physnet.yaml',
			sync: 'Synced',
		},
		{
			name: 'dc-vlan',
			bridge: 'br-dc',
			nodes: ['worker-1', 'worker-2', 'worker-3'],
			nodeCount: 3,
			policy: 'uplink-dc-vlan',
			sourceFile: 'uplinks/dc-vlan.yaml',
			sync: 'Synced',
		},
	],
};

// The same catalog in trouble: the dc-vlan uplink failing on one host.
const netTrouble = {
	...netCatalog,
	uplinks: netCatalog.uplinks.map((u) =>
		u.name === 'dc-vlan'
			? {
					...u,
					failures: [
						{
							node: 'worker-2',
							reason: 'FailedToConfigure',
							message:
								'error reconciling NodeNetworkConfigurationPolicy: VerificationError on br-dc',
						},
					],
				}
			: u,
	),
};

const netPolicies = {
	policies: [
		{
			name: 'deny-lateral',
			kind: 'admin',
			backing: 'AdminNetworkPolicy',
			priority: 10,
			target: 'all namespaces',
			applies: 'every namespace',
			rules: [
				{
					direction: 'Ingress',
					action: 'Deny',
					peer: 'ns any',
					text: 'Blocks all traffic from VMs in any namespace.',
				},
			],
			sourceFile: 'policies/deny-lateral.yaml',
			sync: 'Synced',
		},
		{
			name: 'allow-web-to-db',
			kind: 'dfw',
			namespace: 'db-prod',
			backing: 'NetworkPolicy',
			target: 'app=db-1',
			applies: 'VMs labeled app=db-1 in db-prod',
			isolation: 'Blocks other incoming traffic.',
			rules: [
				{
					direction: 'Ingress',
					action: 'Allow',
					peer: 'ns web-prod pods app=web-1',
					ports: 'TCP/5432',
					text: 'Accepts TCP 5432 from VMs labeled app=web-1 in web-prod.',
				},
			],
			sourceFile: 'policies/allow-web-to-db.yaml',
			sync: 'Synced',
		},
		{
			name: 'allow-https',
			kind: 'dfw',
			namespace: 'web-prod',
			backing: 'NetworkPolicy',
			target: 'all pods',
			applies: 'every VM in web-prod',
			isolation: 'Blocks other incoming traffic.',
			rules: [
				{
					direction: 'Ingress',
					action: 'Allow',
					peer: 'pods any',
					ports: 'TCP/443',
					text: 'Accepts TCP 443 from VMs in web-prod.',
				},
			],
			sourceFile: 'policies/allow-https.yaml',
			sync: 'Synced',
		},
		{
			name: 'default',
			kind: 'gateway',
			namespace: 'batch-prod',
			backing: 'EgressFirewall',
			applies: 'every VM in batch-prod',
			rules: [
				{
					direction: 'Egress',
					action: 'Allow',
					peer: 'mirror.example.com',
					ports: 'TCP/443',
					text: 'Can reach mirror.example.com on TCP 443.',
				},
				{
					direction: 'Egress',
					action: 'Deny',
					peer: '0.0.0.0/0',
					text: 'Cannot reach anything outside.',
				},
			],
			sourceFile: 'policies/egress.yaml',
			sync: 'Synced',
		},
		{
			name: 'web-egress',
			kind: 'egressip',
			backing: 'EgressIP',
			target: 'web-prod',
			applies: 'every VM in web-prod',
			namespaces: ['web-prod'],
			rules: [
				{
					direction: 'Egress',
					action: 'SNAT',
					peer: '192.0.2.10',
					text: 'Leaves with source IP 192.0.2.10.',
				},
			],
			sourceFile: 'egress/web-egress.yaml',
			sync: 'Synced',
		},
		{
			name: 'dmz-route',
			kind: 'route',
			backing: 'AdminPolicyBasedExternalRoute',
			target: 'batch-prod',
			applies: 'every VM in batch-prod',
			namespaces: ['batch-prod'],
			rules: [
				{
					direction: 'Egress',
					action: 'Route',
					peer: 'via 10.0.0.1',
					text: 'Leaves through 10.0.0.1.',
				},
			],
			sourceFile: 'egress/dmz-route.yaml',
			sync: 'Synced',
		},
	],
};

// The platform's own rules on a real cluster: each lets any namespace reach
// its own VMs, so every network matches it without being named (wildcard).
const PLATFORM_RULES = [
	[
		'openshift-gitops',
		'app.kubernetes.io/name=openshift-gitops-application-controller',
		'TCP/8082',
	],
	['openshift-monitoring', 'app.kubernetes.io/name=prometheus', 'TCP/9091'],
	['openshift-dns', 'dns.operator.openshift.io/daemonset-dns=default', 'UDP/53'],
	[
		'openshift-ingress',
		'ingresscontroller.operator.openshift.io/deployment-ingresscontroller=default',
		'TCP/443',
	],
	['openshift-console', 'app=console', 'TCP/8443'],
	['openshift-operators', 'name=gitops-operator', 'TCP/8443'],
];
const platformWildcards = PLATFORM_RULES.map(([ns, target, ports]) => {
	const rule = {
		direction: 'Ingress',
		action: 'Allow',
		peer: 'ns any',
		ports,
		text: `Accepts ${ports.replace('/', ' ')} from VMs in any namespace.`,
	};
	return {
		policy: {
			name: 'allow-from-any-namespace',
			kind: 'dfw',
			namespace: ns,
			backing: 'NetworkPolicy',
			target,
			applies: `VMs labeled ${target} in ${ns}`,
			rules: [rule],
		},
		rule,
		text: `VMs in any namespace can reach VMs labeled ${target} in ${ns} on ${ports.replace('/', ' ')}.`,
		wildcard: true,
	};
});

// Rules elsewhere that name a network's VMs as their other end, by network key.
const netReaches = {
	'web-prod/web-net': [
		{
			policy: netPolicies.policies[1],
			rule: netPolicies.policies[1].rules[0],
			text: 'VMs labeled app=web-1 in web-prod can reach VMs labeled app=db-1 in db-prod on TCP 5432.',
			conditional: true,
		},
		...platformWildcards,
	],
	// A network whose namespace has no rule of its own: only the platform's.
	'batch-prod/udn-tcp-frontend': platformWildcards,
};

// Which extra NICs the base VMs carry, by "namespace/name".
const netNICs = {
	'web-prod/web-1': [{ name: 'lan', network: 'lan-101' }],
	'web-prod/web-2': [{ name: 'blue', network: 'blue-secondary' }],
	'batch-prod/batch-1': [
		{ name: 'l2', network: 'batch-prod/internal-l2' },
		{ name: 'dc', network: 'dc-vlan-net' },
	],
};

// The scale scenario's networking, in the shapes the server renders: rule
// sentences, the failing uplink's host, the rules naming web-net's VMs.
function sentence(r) {
	const ports = r.ports ? r.ports.replaceAll('/', ' ') : '';
	const peer = (r.peer ?? '')
		.replace(/^ns any$/, 'VMs in any namespace')
		.replace(/^namespace (.+)$/, 'VMs in $1')
		.replace(/^([\w.-]+=[\w.-]+)$/, 'VMs labeled $1');
	if (r.action === 'SNAT') return `Leaves with source IP ${r.peer}.`;
	if (r.action === 'Route') return `Leaves through ${r.peer.replace(/^via /, '')}.`;
	if (r.direction === 'Ingress')
		return `${r.action === 'Deny' ? 'Blocks' : 'Accepts'} ${ports || 'all traffic'} from ${peer}.`;
	return `${r.action === 'Deny' ? 'Cannot reach' : 'Can reach'} ${peer}${ports ? ` on ${ports}` : ''}.`;
}

function appliesOf(p) {
	const t = p.target ?? '';
	if (t === 'all namespaces') return 'every namespace';
	if (t === 'all VMs' || t === 'all pods') return `every VM in ${p.namespace}`;
	if (t.startsWith('namespace ')) return `every VM in ${t.slice(10)}`;
	return `VMs labeled ${t} in ${p.namespace}`;
}

function withScaleNetworking(sc) {
	const policies = sc.policies.policies.map((p) => {
		const rules = (p.rules ?? []).map((r) =>
			p.name === 'deny-lateral' ? { ...r, peer: 'ns any' } : r,
		);
		return {
			...p,
			applies: appliesOf(p),
			isolation: p.kind === 'dfw' ? 'Blocks other incoming traffic.' : undefined,
			rules: rules.map((r) => ({ ...r, text: sentence(r) })),
		};
	});
	const uplinks = sc.networks.uplinks.map((u) =>
		u.name === 'dc-vlan'
			? {
					...u,
					failures: [{ node: 'host-41', reason: 'FailedToConfigure', message: 'bond0 not found' }],
				}
			: u,
	);
	const toDB = policies.find((p) => p.name === 'allow-web-to-db');
	return {
		...sc,
		networks: { ...sc.networks, uplinks },
		policies: { policies },
		networkReaches: {
			'web-prod/web-net': [
				{
					policy: toDB,
					rule: toDB.rules[0],
					text: 'VMs labeled app=web in web-prod can reach VMs labeled app=db-1 in db-prod on TCP 5432.',
					conditional: true,
				},
				...platformWildcards,
			],
		},
	};
}

function withNetworking(sc) {
	for (const p of sc.inventory.projects)
		for (const ns of p.namespaces)
			for (const v of ns.vms) {
				const extra = netNICs[`${v.namespace}/${v.name}`];
				if (extra) v.networks = [...(v.networks ?? []), ...extra];
			}
	return { ...sc, networks: netCatalog, policies: netPolicies, networkReaches: netReaches };
}

// ── base: a small healthy fleet ────────────────────────────────────────────────

let base = {
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
						canAuthor: true,
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
						canAuthor: true,
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
						canAuthor: true,
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
				branch: 'dotvirt/proposed/admin/team-db-1a2b',
				by: 'admin',
				mine: true,
				checks: 'success',
				approvals: 0,
				requiredApprovals: 1,
			},
			{
				project: 'team-web',
				prNumber: 42,
				prURL: 'https://forge.example/dotvirt/team-web/pulls/42',
				title: 'web-1: exclude from the balancer',
				branch: 'dotvirt/proposed/bob/team-web-9f9f',
				by: 'bob',
				checks: 'pending',
				approvals: 0,
				requiredApprovals: 1,
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
	storageClasses,
	// The create sheet's additions (create.mjs): an overlapping pair of
	// networks and a library to deploy from.
	networks: { ...networks, networks: [...networks.networks, ...createNetworks] },
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

base = withNetworking(base);

// ── storage (S3): disk lineage, snapshots and claims over base ──────────────────
// Kept as one block that patches base, so the other streams' edits to base do
// not collide with it. Disk counts per VM stay as they were except web-2's
// data disk (the merged "add data disk" change in history) and db-1's data
// disk on the local class beside a shared root.

const IMAGES_NS = 'dotvirt-images';
const img = (name) => `${IMAGES_NS}/${name}`;
const rootdisk = (vmName, size, image, over = {}) => ({
	name: 'rootdisk',
	type: 'dataVolume',
	size,
	claim: `${vmName}-rootdisk`,
	image: img(image),
	...over,
});
const cloudinit = { name: 'cloudinitdisk', type: 'cloudInitNoCloud' };

function patchDisks(scenario, disksByVM) {
	for (const p of scenario.inventory.projects)
		for (const n of p.namespaces)
			for (const v of n.vms) if (disksByVM[v.name]) v.disks = disksByVM[v.name];
}

patchDisks(base, {
	'web-1': [rootdisk('web-1', '30Gi', 'fedora-42'), cloudinit],
	'web-2': [
		rootdisk('web-2', '30Gi', 'fedora-42'),
		{ name: 'data', type: 'dataVolume', size: '50Gi', claim: 'web-2-data' },
		cloudinit,
	],
	'db-1': [
		rootdisk('db-1', '40Gi', 'centos-stream-9', { storageClass: 'bulk-hdd' }),
		{ name: 'data', type: 'dataVolume', size: '100Gi', claim: 'db-1-data' },
	],
	'batch-1': [rootdisk('batch-1', '20Gi', 'rhel-9.6'), cloudinit],
});

options.osImages.push(
	{ name: 'centos-stream-9', namespace: IMAGES_NS, ready: true },
	{ name: 'rhel-9.6', namespace: IMAGES_NS, ready: true },
	{ name: 'windows-2025', namespace: IMAGES_NS, ready: true },
);

const libraryTemplate = (name, image) => ({
	name,
	library: 'platform',
	sourceFile: `templates/${name}.yaml`,
	instancetype: 'u1.medium',
	preference: 'fedora',
	image: img(image),
	yaml: `apiVersion: template.kubevirt.io/v1beta1\nkind: VirtualMachineTemplate\nmetadata:\n  name: ${name}\n`,
});
base.templates = {
	templates: [
		libraryTemplate('fedora-server', 'fedora-42'),
		libraryTemplate('fedora-minimal', 'fedora-42'),
		libraryTemplate('centos-stream-server', 'centos-stream-9'),
		libraryTemplate('rhel-9-server', 'rhel-9.6'),
		libraryTemplate('windows-2025-server', 'windows-2025'),
		// The create sheet's project library template (create.mjs).
		...createTemplates.templates,
	],
};

const vmSnapshot = (namespace, vmName, name, minsAgo, volumes, over = {}) => ({
	namespace,
	vm: vmName,
	name,
	created: iso(minsAgo),
	phase: 'Succeeded',
	readyToUse: true,
	indications: ['Online', 'GuestAgent'],
	volumes,
	...over,
});
const DAY = 1440;
base.storageSnapshots = [
	vmSnapshot('web-prod', 'web-1', 'before-release-41', 120, ['rootdisk']),
	vmSnapshot('web-prod', 'web-1', 'web-1-20261106', DAY + 60, ['rootdisk']),
	vmSnapshot('web-prod', 'web-1', 'before-php-8.4', 4 * DAY, ['rootdisk']),
	...Array.from({ length: 9 }, (_, i) =>
		vmSnapshot('web-prod', 'web-1', `web-1-daily-${i + 1}`, (5 + i) * DAY, ['rootdisk']),
	),
	vmSnapshot('web-prod', 'web-2', 'web-2-before-data', 9 * DAY, ['rootdisk']),
	vmSnapshot('db-prod', 'db-1', 'db-1-nightly', DAY, ['rootdisk', 'data']),
	vmSnapshot('db-prod', 'db-1', 'db-1-adhoc', 30, ['rootdisk', 'data'], {
		phase: 'Failed',
		readyToUse: false,
		error: 'VolumeSnapshot db-1-data failed: snapshot class not ready',
	}),
];

const claim = (namespace, name, storageClass, capacity, node) => ({
	namespace,
	name,
	storageClass,
	capacity,
	phase: 'Bound',
	...(node ? { node } : {}),
});
base.storageClaims = [
	claim('web-prod', 'web-1-rootdisk', 'fast-ssd', '30Gi', 'worker-1'),
	claim('web-prod', 'web-2-rootdisk', 'fast-ssd', '30Gi', 'worker-3'),
	claim('web-prod', 'web-2-data', 'fast-ssd', '50Gi', 'worker-3'),
	claim('db-prod', 'db-1-rootdisk', 'bulk-hdd', '40Gi'),
	claim('db-prod', 'db-1-data', 'fast-ssd', '100Gi', 'worker-1'),
	claim('batch-prod', 'batch-1-rootdisk', 'fast-ssd', '20Gi', 'worker-2'),
];

// dupnames: one VM name in two namespaces, as on a real cluster where two
// teams each made a "test". Storage rows must tell them apart.
const qaVM = (namespace, node) =>
	vm(namespace, 'test', {
		nodeName: node,
		disks: [rootdisk('test', '20Gi', 'fedora-42'), cloudinit],
	});
const dupnames = {
	...base,
	name: 'dupnames',
	inventory: {
		...base.inventory,
		projects: [
			...base.inventory.projects,
			{
				name: 'team-qa',
				repo: 'https://forge.example/dotvirt/team-qa.git',
				gitOps: { sync: 'Synced', health: 'Healthy', operation: 'Succeeded', revision: 'aa11bb2' },
				namespaces: [
					{ namespace: 'qa-a', canAuthor: true, vms: [qaVM('qa-a', 'worker-1')] },
					{ namespace: 'qa-b', canAuthor: true, vms: [qaVM('qa-b', 'worker-2')] },
				],
			},
		],
	},
	storageSnapshots: [
		...base.storageSnapshots,
		vmSnapshot('qa-a', 'test', 'test-before-upgrade', 90, ['rootdisk']),
		vmSnapshot('qa-b', 'test', 'test-nightly', 300, ['rootdisk']),
	],
	storageClaims: [
		...base.storageClaims,
		claim('qa-a', 'test-rootdisk', 'fast-ssd', '20Gi', 'worker-1'),
		claim('qa-b', 'test-rootdisk', 'fast-ssd', '20Gi', 'worker-2'),
	],
};

// ── empty: pristine install, nothing visible yet ───────────────────────────────

const empty = {
	...base,
	name: 'empty',
	storageSnapshots: [],
	storageClaims: [],
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
						canAuthor: true,
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
			uplink: false,
			namespace: true,
			egressIP: false,
			externalRoute: false,
			adminNetworkPolicy: false,
			platform: false,
		},
	},
	// The optional planes answer 503 (down), not empty (fine): the UI must
	// degrade with a visible note, never a blank pane or a crash.
	alarms: null,
	metrics: null,
};

// storage (S3): the live snapshot and claim reads fail with the cluster.
degraded.storageSnapshots = null;
degraded.storageClaims = null;

// ── drift: every GitOps trouble state at once ──────────────────────────────────
// Standing sync failure (stays after the operation ends), Pending (declared,
// awaiting first sync), NotTracked (cluster-only, adoptable), a prune warning
// on the draft, and an adoptable legacy namespace.

const drift = {
	...base,
	name: 'drift',
	networks: netTrouble,
	inventory: {
		// The platform tier's standing problem: an uplink policy nmstate cannot
		// apply. It must reach the issues plane by name, with a link.
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
					message: 'FailedToConfigure: 1/1 nodes failed to configure',
				},
			],
		},
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
						canAuthor: true,
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
						canAuthor: true,
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
					changes: [{ field: 'Memory', action: 'change', from: '4Gi', to: '8Gi', restart: true }],
					edit: { memory: '8Gi' },
				},
			],
			warning:
				'Merging will prune 2 objects the repo no longer declares (web-prod/old-vm, web-prod/old-net). Verify they are meant to go.',
			prune: [
				{ kind: 'VirtualMachine', namespace: 'web-prod', name: 'old-vm' },
				{ kind: 'NetworkAttachmentDefinition', namespace: 'web-prod', name: 'old-net' },
			],
		},
	},
	vmDrift: {
		'web-prod/web-1': {
			drift: true,
			changes: [{ field: 'CPU', action: 'change', from: '2 vCPU', to: '4 vCPU' }],
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

// ── residue: a labeled project with no repo (an old install's leftover) ────────
// The "no repo configured" dead-end Release resolves: the project label stands
// on namespaces nothing in git describes.
const residue = {
	...base,
	name: 'residue',
	inventory: {
		...base.inventory,
		projects: [
			...base.inventory.projects,
			{
				name: 'oldportal',
				repo: '',
				namespaces: [
					{
						namespace: 'oldportal-ns',
						canAuthor: true,
						vms: [vm('oldportal-ns', 'stray-1', { sync: 'NotTracked', sourceFile: undefined })],
					},
				],
			},
		],
	},
};

export const scenarios = {
	residue,
	sso,
	ssoready,
	base,
	empty,
	degraded,
	drift,
	large: largeScenario(),
	// A full platform at the boards' numbers (2,846 VMs, 131 networks, 3,412
	// disks); see scale.mjs.
	scale: withScaleNetworking(scaleScenario(base)),
	dupnames,
};

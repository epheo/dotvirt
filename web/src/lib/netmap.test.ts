import { describe, expect, it } from 'vitest';
import type { Network, Policy, Project, Uplink, VM } from '#lib/api.js';
import {
	clusterMap,
	cutOffHosts,
	membership,
	networkProblems,
	searchMap,
	wayOut,
	type MapInput,
} from './netmap';

function vm(namespace: string, name: string, over: Partial<VM> = {}): VM {
	return {
		namespace,
		name,
		power: 'On',
		sourceFile: `vms/${name}.yaml`,
		sync: 'Synced',
		networks: [{ name: 'default', network: 'pod' }],
		...over,
	};
}

const net = (over: Partial<Network> & { name: string }): Network => ({
	kind: 'internal',
	scope: 'project',
	backing: 'UserDefinedNetwork',
	...over,
});

const networks: Network[] = [
	net({
		name: 'web-net',
		kind: 'default',
		namespace: 'web-prod',
		attachRef: 'web-prod/web-net',
		subnets: ['10.40.0.0/16'],
	}),
	net({
		name: 'udn-frontend',
		namespace: 'default',
		attachRef: 'default/udn-frontend',
		subnets: ['10.200.0.0/16'],
	}),
	net({
		name: 'udn-tcp-frontend',
		namespace: 'default',
		attachRef: 'default/udn-tcp-frontend',
		subnets: ['10.200.0.0/16'],
	}),
	// Same range, other namespace: isolated, never an overlap.
	net({
		name: 'reuse',
		namespace: 'db-prod',
		attachRef: 'db-prod/reuse',
		subnets: ['10.200.0.0/16'],
	}),
	net({
		name: 'blue-secondary',
		scope: 'shared',
		attachRef: 'blue-secondary',
		subnets: ['10.222.1.0/24'],
		namespaces: ['web-prod', 'db-prod'],
	}),
	net({
		name: 'green-secondary',
		scope: 'shared',
		attachRef: 'green-secondary',
		subnets: ['10.222.2.0/24'],
	}),
	net({
		name: 'lan-101',
		kind: 'vlan',
		scope: 'shared',
		attachRef: 'lan-101',
		vlan: 101,
		uplink: 'physnet',
		namespaces: ['web-prod', 'db-prod'],
	}),
	net({
		name: 'dc-vlan-net',
		kind: 'vlan',
		scope: 'shared',
		attachRef: 'dc-vlan-net',
		uplink: 'dc-vlan',
	}),
];

const uplinks: Uplink[] = [
	{ name: 'br-ex', bridge: 'br-ex', builtin: true, nodes: ['w1', 'w2', 'w3'], nodeCount: 3 },
	{ name: 'physnet', bridge: 'br-physnet', nodes: ['w1', 'w2'], nodeCount: 2 },
	{
		name: 'dc-vlan',
		bridge: 'br-dc',
		nodes: ['worker-2'],
		nodeCount: 1,
		failures: [{ node: 'worker-2', reason: 'FailedToConfigure' }],
	},
];

const projects: Project[] = [
	{
		name: 'team-web',
		namespaces: [
			{
				namespace: 'web-prod',
				vms: [
					vm('web-prod', 'web-1', {
						nodeName: 'worker-1',
						guestIP: '10.40.0.5',
						networks: [
							{ name: 'default', network: 'pod' },
							{ name: 'lan', network: 'lan-101' },
							{ name: 'blue', network: 'blue-secondary' },
						],
					}),
					vm('web-prod', 'web-2'),
				],
			},
		],
	},
	{
		name: 'team-db',
		namespaces: [
			{
				namespace: 'db-prod',
				vms: [
					vm('db-prod', 'db-1', { nodeName: 'worker-1', guestIP: '10.128.0.12' }),
					vm('db-prod', 'vm-a', {
						networks: [{ name: 'dc', network: 'dc-vlan-net' }],
						nodeName: 'worker-3',
					}),
					vm('db-prod', 'bare', { networks: undefined }),
				],
			},
		],
	},
	{ name: 'tiny', namespaces: [{ namespace: 'tiny', vms: [vm('tiny', 't-1')] }] },
];
const vms = projects.flatMap((p) => p.namespaces.flatMap((n) => n.vms));

const policies = [
	{
		name: 'web-egress',
		kind: 'egressip',
		backing: 'EgressIP',
		rules: [{ direction: 'Egress', action: 'SNAT', peer: '192.0.2.10' }],
	},
	{
		name: 'dmz',
		kind: 'route',
		backing: 'AdminPolicyBasedExternalRoute',
		rules: [{ direction: 'Egress', action: 'Route', peer: 'via 10.0.0.1' }],
	},
] as Policy[];

function input(over: Partial<MapInput> = {}): MapInput {
	const members = membership(vms, networks);
	return {
		networks,
		uplinks,
		projects,
		policies,
		members,
		problems: networkProblems(networks, uplinks),
		...over,
	};
}

describe('membership', () => {
	it('counts VMs per network and the ones only on the pod network', () => {
		const m = membership(vms, networks);
		expect(m.byNet.get('web-prod/web-net')?.map((v) => v.name)).toEqual(['web-1', 'web-2']);
		expect(m.byNet.get('lan-101')?.length).toBe(1);
		// db-1, t-1 and bare (implicit pod interface) ride only the pod network.
		expect(m.podOnly).toBe(3);
	});
});

describe('networkProblems', () => {
	it('reports a failing uplink and an overlap only where the networks can meet', () => {
		const ps = networkProblems(networks, uplinks);
		expect(ps.map((p) => [p.kind, p.title])).toEqual([
			['uplink', 'Fails on worker-2'],
			['overlap', 'Overlap'],
		]);
		expect(ps[1].detail).toBe('udn-frontend and udn-tcp-frontend both use 10.200.0.0/16.');
		expect(ps[1].networks).toEqual(['default/udn-frontend', 'default/udn-tcp-frontend']);
	});
	it('counts a VM on both networks as meeting', () => {
		const both = vm('db-prod', 'bridge', {
			networks: [
				{ name: 'r', network: 'db-prod/reuse' },
				{ name: 'f', network: 'default/udn-frontend' },
			],
		});
		const ps = networkProblems(networks, [], membership([both], networks));
		expect(ps[0].networks).toEqual([
			'db-prod/reuse',
			'default/udn-frontend',
			'default/udn-tcp-frontend',
		]);
	});
	it('names the hosts a VLAN network is cut off from', () => {
		expect(cutOffHosts(networks[7], uplinks)).toEqual(['worker-2']);
		expect(cutOffHosts(networks[6], uplinks)).toEqual([]);
	});
});

describe('clusterMap', () => {
	it("draws every uplink, each one's biggest network and every problem", () => {
		const m = clusterMap(input({ netBudget: 3, projectBudget: 2 }));
		expect(m.rows.map((r) => r.label)).toEqual(['default', 'dc-vlan', 'physnet']);
		expect(m.rows[0].allHosts).toBe(true);
		expect(m.rows[2].allHosts).toBe(false);
		expect(m.rows[1].problem?.title).toBe('Fails on worker-2');
		expect(m.rows.map((r) => r.nets.map((n) => n.net.name))).toEqual([
			['blue-secondary'],
			['dc-vlan-net'],
			['lan-101'],
		]);
		expect(m.rows[1].nets[0].cutOff).toEqual(['worker-2']);
		expect(m.moreNetworks).toEqual({ count: 1, vms: 0, shared: 1, vlan: 0 });
		expect(m.edge).toEqual({ fixedIPs: 1, routes: 1 });
		expect(m.podOnly).toBe(3);
	});
	it('keeps the biggest projects and folds the rest with their sums', () => {
		const m = clusterMap(input({ projectBudget: 2 }));
		expect(m.projects.map((p) => [p.name, p.vms, p.networks])).toEqual([
			['team-db', 3, 1],
			['team-web', 2, 1],
		]);
		expect(m.moreProjects).toMatchObject({ count: 1, vms: 1, networks: 0 });
		expect(m.shown).toEqual({ networks: 4, networksTotal: 4, projects: 2, projectsTotal: 3 });
	});
	it("forces the focused VM's project and networks in", () => {
		const t1 = projects[2].namespaces[0].vms[0];
		const m = clusterMap(input({ projectBudget: 1, focus: { kind: 'vm', vm: t1 } }));
		expect(m.projects.map((p) => p.name)).toEqual(['tiny']);
	});
	it('shows only problems when asked', () => {
		const m = clusterMap(input({ problemsOnly: true }));
		expect(m.rows.flatMap((r) => r.nets.map((n) => n.net.name))).toEqual(['dc-vlan-net']);
		expect(m.moreNetworks).toBeNull();
		expect(m.moreProjects).toBeNull();
	});
	it('names the uplinks VLAN networks use when the fabric is unreadable', () => {
		const m = clusterMap(input({ uplinks: [] }));
		expect(m.rows.map((r) => [r.label, r.known])).toEqual([
			['default', false],
			['dc-vlan', false],
			['physnet', false],
		]);
	});
});

describe('searchMap', () => {
	it('finds VMs by name and address, networks by name and range', () => {
		expect(searchMap('db-1', vms, networks)?.summary).toBe('1 VM');
		expect(searchMap('10.40.0.5', vms, networks)).toMatchObject({ summary: '1 VM, 1 network' });
		expect(searchMap('web', vms, networks)?.summary).toBe('2 VMs, 1 network');
		expect(searchMap('nothing-here', vms, networks)?.summary).toBe('No match');
		expect(searchMap('  ', vms, networks)).toBeNull();
	});
	it('ranks an exact name first', () => {
		const more = [...vms, vm('db-prod', 'db-10')];
		expect(searchMap('db-1', more, networks)?.vms[0].name).toBe('db-1');
	});
});

describe('wayOut', () => {
	it('leaves a pod-network VM through the default uplink and the cluster edge', () => {
		const w = wayOut(vms[2], networks, uplinks);
		expect(w.steps.map((s) => s.text)).toEqual([
			'Pod network',
			'default uplink on worker-1',
			"Cluster edge. Source IP becomes worker-1's.",
			'Internet',
		]);
		expect(w).toMatchObject({
			via: 'edge',
			network: 'Pod network',
			uplink: 'br-ex',
			host: 'worker-1',
		});
	});
	it('starts on the primary project network and names other VLAN paths', () => {
		const w = wayOut(vms[0], networks, uplinks);
		expect(w.steps[0].text).toBe('web-net');
		expect(w.network).toBe('web-prod/web-net');
		expect(w.also.map((n) => n.name)).toEqual(['lan-101']);
	});
	it('names a fixed outbound IP or an external route at the edge', () => {
		expect(wayOut(vms[2], networks, uplinks, { snat: policies[0] }).steps[2].text).toBe(
			'Cluster edge. Source IP becomes 192.0.2.10, fixed outbound IP web-egress.',
		);
		expect(wayOut(vms[2], networks, uplinks, { route: policies[1] }).steps[2].text).toBe(
			'Cluster edge. Leaves through 10.0.0.1, external route dmz.',
		);
	});
	it('takes a VLAN-only VM straight to the fabric', () => {
		const w = wayOut(vms[3], networks, uplinks);
		expect(w.via).toBe('fabric');
		expect(w.steps.map((s) => s.text)).toEqual([
			'dc-vlan-net',
			'dc-vlan uplink on worker-3',
			'Datacenter fabric. The VM keeps its own IP.',
		]);
	});
	it('says when a network has no way out', () => {
		const iso = vm('default', 'iso', {
			networks: [{ name: 'fe', network: 'default/udn-frontend' }],
		});
		expect(wayOut(iso, networks, uplinks)).toMatchObject({
			via: 'none',
			network: 'default/udn-frontend',
		});
	});
});

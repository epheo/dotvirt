import { describe, expect, it } from 'vitest';
import type { Inventory, VM } from '#lib/api.js';
import {
	alertLabel,
	deriveIssues,
	deriveSuggestions,
	issuesInScope,
	issueCountByProject,
} from '#lib/issues.js';

function vm(over: Partial<VM>): VM {
	return {
		namespace: 'ns-a',
		name: 'vm-a',
		power: 'On',
		sourceFile: 'vms/vm-a.yaml',
		sync: 'Synced',
		...over,
	};
}

function inv(projects: Inventory['projects']): Inventory {
	return { projects };
}

describe('deriveIssues', () => {
	it('returns nothing for a healthy inventory', () => {
		const i = inv([
			{
				name: 'p1',
				namespaces: [{ namespace: 'ns-a', vms: [vm({ phase: 'Running', health: 'Healthy' })] }],
			},
		]);
		expect(deriveIssues(i)).toEqual([]);
		expect(deriveIssues(null)).toEqual([]);
	});

	it('ignores transitional states: OutOfSync, Progressing, Migrating', () => {
		const i = inv([
			{
				name: 'p1',
				gitOps: { sync: 'OutOfSync', operation: 'Running', health: 'Progressing' },
				namespaces: [{ namespace: 'ns-a', vms: [vm({ sync: 'OutOfSync', phase: 'Migrating' })] }],
			},
		]);
		expect(deriveIssues(i)).toEqual([]);
	});

	it("folds a VM's reasons into one row, highest severity first overall", () => {
		const i = inv([
			{
				name: 'p1',
				error: 'no usable repo',
				namespaces: [
					{
						namespace: 'ns-a',
						vms: [
							vm({ name: 'broken', syncError: 'webhook denied', health: 'Degraded' }),
							vm({ name: 'sad', health: 'Degraded' }),
						],
					},
				],
			},
		]);
		const issues = deriveIssues(i);
		expect(issues.map((x) => [x.scope, x.severity])).toEqual([
			['ns-a/broken', 'danger'],
			['ns-a/sad', 'warn'],
			['p1', 'warn'],
		]);
		expect(issues[0].label).toBe('apply failed, degraded');
		expect(issues[0].detail).toBe('webhook denied');
	});

	it('retires a failed operation once the app has converged', () => {
		const i = inv([
			{
				name: 'p1',
				gitOps: { sync: 'Synced', operation: 'Failed' },
				namespaces: [{ namespace: 'ns-a', vms: [vm({ name: 'ok', sync: 'Synced' })] }],
			},
		]);
		expect(deriveIssues(i)).toHaveLength(0);
	});

	it('flags failed syncs and error-family phases', () => {
		const i = inv([
			{
				name: 'p1',
				gitOps: { operation: 'Failed', syncError: 'apply refused' },
				namespaces: [
					{ namespace: 'ns-a', vms: [vm({ name: 'stuck', phase: 'ErrorUnschedulable' })] },
				],
			},
		]);
		const issues = deriveIssues(i);
		expect(issues).toHaveLength(2);
		expect(issues.every((x) => x.severity === 'danger')).toBe(true);
	});
});

describe('scope helpers', () => {
	const issues = deriveIssues(
		inv([
			{
				name: 'p1',
				namespaces: [{ namespace: 'ns-a', vms: [vm({ name: 'x', health: 'Degraded' })] }],
			},
			{
				name: 'p2',
				error: 'broken',
				namespaces: [
					{ namespace: 'ns-b', vms: [vm({ name: 'y', namespace: 'ns-b', syncError: 'nope' })] },
				],
			},
		]),
	);

	it('filters by project and namespace (project-level issues stay visible)', () => {
		expect(issuesInScope(issues, {}).length).toBe(3);
		expect(issuesInScope(issues, { project: 'p2' }).length).toBe(2);
		expect(issuesInScope(issues, { project: 'p2', namespace: 'ns-b' }).map((i) => i.scope)).toEqual(
			['ns-b/y', 'p2'],
		);
	});

	it('counts per project for the tree rollup', () => {
		const counts = issueCountByProject(issues);
		expect(counts.get('p1')).toBe(1);
		expect(counts.get('p2')).toBe(2);
	});

	it('names each degraded or refused object and links to its view', () => {
		const i: Inventory = {
			projects: [
				{
					name: 'p1',
					gitOps: {
						sync: 'Synced',
						health: 'Degraded',
						unhealthy: [
							{ kind: 'UserDefinedNetwork', namespace: 'ns-a', name: 'net-a', health: 'Degraded' },
							{ kind: 'VirtualMachine', namespace: 'ns-a', name: 'vm-a', health: 'Degraded' },
						],
					},
					namespaces: [{ namespace: 'ns-a', vms: [] }],
				},
			],
			platform: {
				sync: 'Synced',
				health: 'Degraded',
				unhealthy: [
					{
						kind: 'NodeNetworkConfigurationPolicy',
						name: 'dc-vlan-bridge',
						health: 'Degraded',
						message: 'FailedToConfigure: 1/1 nodes failed to configure',
					},
				],
			},
		};
		const issues = deriveIssues(i);
		// The VM is vmIssue's to report, so only the network and the policy remain.
		expect(issues.map((x) => [x.scope, x.label, x.href, x.project, x.severity])).toEqual([
			[
				'dc-vlan-bridge',
				'NodeNetworkConfigurationPolicy degraded',
				'/networking?tab=uplinks',
				'platform',
				'warn',
			],
			['ns-a/net-a', 'UserDefinedNetwork degraded', '/networking/ns-a/net-a', 'p1', 'warn'],
		]);
		expect(issues[0].detail).toBe('FailedToConfigure: 1/1 nodes failed to configure');
	});

	it('reports a refused platform apply as the failure and the object it refused', () => {
		const i: Inventory = {
			projects: [],
			platform: {
				sync: 'OutOfSync',
				health: 'Healthy',
				operation: 'Failed',
				syncError: 'one or more synchronization tasks completed unsuccessfully',
				unhealthy: [
					{
						kind: 'Namespace',
						name: 'tenant-a',
						message: 'namespaces "tenant-a" is forbidden: label cannot be removed',
					},
				],
			},
		};
		const issues = deriveIssues(i);
		expect(issues.map((x) => [x.scope, x.label, x.href, x.severity])).toEqual([
			['platform', 'Sync failed', '/changes', 'danger'],
			['tenant-a', 'Namespace apply failed', '/compute', 'danger'],
		]);
		expect(issues[1].detail).toMatch(/forbidden/);
		// Platform issues never leak into a project's own scope.
		expect(issuesInScope(issues, { project: 'p1' })).toEqual([]);
	});
});

describe('next steps', () => {
	it('offers the repo dialog only to callers who may run it', () => {
		const i = inv([
			{ name: 'p1', error: 'no usable repo', namespaces: [{ namespace: 'ns-a', vms: [] }] },
		]);
		const [repair] = deriveIssues(i, { canRepair: () => true });
		expect(repair.action).toEqual({
			label: 'Attach repo',
			modal: { kind: 'adoptProject', project: 'p1', namespaces: ['ns-a'], recover: false },
		});
		expect(deriveIssues(i)[0].action).toEqual({ label: 'Open project', href: '/compute/p1' });
	});

	it('reads a lost repo as recoverable, and a converged app as healthy', () => {
		const lost = inv([
			{
				name: 'p1',
				repo: 'https://forge/p1',
				gitOps: { sync: 'Unknown', syncError: 'repository not found' },
				namespaces: [],
			},
		]);
		expect(deriveIssues(lost, { canRepair: () => true })[0].action.label).toBe('Recover repo');
		const converged = inv([
			{
				name: 'p1',
				repo: 'https://forge/p1',
				gitOps: { sync: 'Synced', operation: 'Failed', syncError: 'an old refused apply' },
				namespaces: [],
			},
		]);
		expect(deriveIssues(converged, { canRepair: () => true })).toEqual([]);
	});
});

describe('alerts', () => {
	const fleet = inv([
		{ name: 'p1', namespaces: [{ namespace: 'ns-a', vms: [vm({ phase: 'Running' })] }] },
	]);

	it('joins firing alerts to the plane at their severity, on the Monitor tab', () => {
		const issues = deriveIssues(fleet, {
			alerts: [
				{
					name: 'KubeVirtVMIExcessiveMigrations',
					severity: 'warning',
					namespace: 'ns-a',
					vm: 'vm-a',
					count: 3,
				},
				{ name: 'KubeVirtNoAvailableNodesToRunVMs', severity: 'critical' },
				{ name: 'KubeVirtDeprecatedAPIRequested', severity: 'info' },
			],
		});
		expect(issues.map((x) => [x.severity, x.scope, x.label, x.href, x.project])).toEqual([
			['danger', 'cluster', 'No available nodes to run VMs', '/compute?tab=monitor', ''],
			['warn', 'ns-a/vm-a', 'VMI excessive migrations', '/vm/ns-a/vm-a?tab=monitor', 'p1'],
		]);
		expect(issues[1].detail).toBe('Cluster alert KubeVirtVMIExcessiveMigrations, firing 3 times');
		expect(issues[1].action).toEqual({ label: 'Open monitor', href: '/vm/ns-a/vm-a?tab=monitor' });
	});

	it('keeps a namespace alert inside its namespace scope', () => {
		const issues = deriveIssues(fleet, {
			alerts: [{ name: 'KubeVirtVMStuckInErrorState', severity: 'warning', namespace: 'ns-a' }],
		});
		expect(issuesInScope(issues, { project: 'p1', namespace: 'ns-a' })).toHaveLength(1);
		expect(issuesInScope(issues, { project: 'p1', namespace: 'ns-b' })).toHaveLength(0);
	});

	it('reads alert names as words', () => {
		expect(alertLabel('KubeVirtVMIExcessiveMigrations')).toBe('VMI excessive migrations');
		expect(alertLabel('KubeVirtVMStuckInErrorState')).toBe('VM stuck in error state');
	});
});

describe('deriveSuggestions', () => {
	it('suggests adoption, never as an issue, gated like the stage routes', () => {
		const i: Inventory = {
			projects: [
				{
					name: 'p1',
					namespaces: [
						{ namespace: 'ns-a', vms: [vm({ name: 'loose', sync: 'NotTracked' })] },
						{
							namespace: 'ns-ro',
							vms: [vm({ namespace: 'ns-ro', name: 'ro', sync: 'NotTracked' })],
						},
					],
				},
			],
			adoptable: [{ namespace: 'legacy', vms: 2 }],
		};
		const s = deriveSuggestions(i, (ns) => ns === 'ns-a');
		expect(s.map((x) => [x.scope, x.label, x.action.label])).toEqual([
			['legacy', '2 VMs outside any project', 'Adopt'],
			['ns-a/loose', 'running, not in git', 'Adopt into git'],
		]);
		expect(s.every((x) => x.severity === 'info')).toBe(true);
		expect(deriveIssues(i)).toEqual([]);
	});
});

describe('network problems in the issues plane', () => {
	it('names the uplink and the overlapping networks, and drops the repeated policy rollup', () => {
		const inv: Inventory = {
			projects: [{ name: 'p1', namespaces: [{ namespace: 'ns-a', vms: [] }] }],
			platform: {
				sync: 'Synced',
				health: 'Degraded',
				unhealthy: [
					{ kind: 'NodeNetworkConfigurationPolicy', name: 'uplink-dc', health: 'Degraded' },
				],
			},
		};
		const issues = deriveIssues(inv, {
			network: [
				{
					id: 'uplink:dc',
					kind: 'uplink',
					severity: 'danger',
					title: 'Fails on host-41',
					detail: 'dc is not configured on host-41: FailedToConfigure.',
					uplink: 'dc',
					policy: 'uplink-dc',
					hosts: ['host-41'],
				},
				{
					id: 'overlap:x',
					kind: 'overlap',
					severity: 'warn',
					title: 'Overlap',
					detail: 'a and b both use 10.200.0.0/16.',
					networks: ['ns-a/a', 'ns-a/b'],
				},
			],
		});
		expect(issues.map((x) => [x.scope, x.label, x.href, x.project, x.severity])).toEqual([
			['dc', 'fails on host-41', '/networking?tab=uplinks', 'platform', 'danger'],
			['a and b', 'subnets overlap', '/networking/ns-a/a', 'p1', 'warn'],
		]);
	});
});

describe('objects a project repo cannot keep', () => {
	it("says a kind is this install's rule when tenant repos may apply kinds beyond the listed ones", () => {
		const inv: Inventory = {
			projects: [
				{
					name: 'team-web',
					namespaces: [{ namespace: 'web-prod', vms: [] }],
					platformKinds: [{ kind: 'Role', namespace: 'web-prod', name: 'widen' }],
				},
			],
		};
		expect(deriveIssues(inv, { extraTenantKinds: true }).map((i) => i.label)).toEqual([
			"Role this install's tenant repos may not apply",
		]);
		expect(deriveIssues(inv).map((i) => i.label)).toEqual(['Role only the platform repo may hold']);
	});

	it('names a file Argo reads as source settings, to remove in git', () => {
		const inv: Inventory = {
			projects: [
				{
					name: 'team-web',
					namespaces: [{ namespace: 'web-prod', vms: [] }],
					sourceOverrides: ['.argocd-source.yaml'],
				},
			],
		};
		expect(
			deriveIssues(inv).map((i) => [i.scope, i.label, i.severity, i.project, i.action.label]),
		).toEqual([
			[
				'.argocd-source.yaml',
				'Argo source settings in git',
				'danger',
				'team-web',
				"Open the project's changes",
			],
		]);
	});

	it('names each by object, with its next action', () => {
		const inv: Inventory = {
			projects: [
				{
					name: 'team-web',
					namespaces: [{ namespace: 'web-prod', vms: [] }],
					unnamespaced: [{ kind: 'NetworkPolicy', namespace: 'web-prod', name: 'allow-https' }],
					outOfProject: [{ kind: 'VirtualMachine', namespace: 'db-prod', name: 'web-9' }],
					platformKinds: [
						{ kind: 'NetworkAttachmentDefinition', namespace: 'web-prod', name: 'legacy-l2' },
					],
				},
			],
		};
		const issues = deriveIssues(inv);
		expect(issues.map((i) => [i.scope, i.label, i.severity, i.action.label])).toEqual([
			['db-prod/web-9', 'VirtualMachine outside team-web', 'danger', "Open the project's changes"],
			[
				'web-prod/legacy-l2',
				'NetworkAttachmentDefinition only the platform repo may hold',
				'danger',
				'Open the network',
			],
			[
				'web-prod/allow-https',
				'NetworkPolicy has no namespace in git',
				'warn',
				'Set its namespace',
			],
		]);
		const unns = issues.find((i) => i.label.includes('no namespace'))!;
		expect(unns.action).toEqual({
			label: 'Set its namespace',
			edit: { resource: 'networkpolicy', namespace: 'web-prod', name: 'allow-https' },
		});
		expect(issues[1].href).toBe('/networking/web-prod/legacy-l2');
	});
});

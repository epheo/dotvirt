import { describe, expect, it } from 'vitest';
import type { Inventory, VM } from '$lib/api';
import { deriveIssues, issuesInScope, issueCountByProject } from '$lib/issues';

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
});

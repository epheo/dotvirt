import { describe, expect, it } from 'vitest';
import { buildTasks, type TaskSources } from './tasks';
import type { Inventory } from './api';

const now = Date.parse('2026-07-27T12:00:00Z');

function inv(vm: Partial<Inventory['projects'][0]['namespaces'][0]['vms'][0]>): Inventory {
	return {
		projects: [
			{
				name: 'team-a',
				namespaces: [
					{
						name: 'tenant-a',
						vms: [{ namespace: 'tenant-a', name: 'web', sync: 'Synced', ...vm }],
					},
				],
			},
		],
	} as unknown as Inventory;
}

function sources(over: Partial<TaskSources>): TaskSources {
	return { inventory: null, feed: [], drafts: [], proposals: [], username: 'alice', ...over };
}

describe('buildTasks', () => {
	it('keeps a finished migration only within the linger window', () => {
		const recent = inv({
			migration: { completed: true, endedAt: new Date(now - 5 * 60 * 1000).toISOString() },
		} as never);
		const old = inv({
			migration: { completed: true, endedAt: new Date(now - 20 * 60 * 1000).toISOString() },
		} as never);
		expect(buildTasks(sources({ inventory: recent }), now).map((t) => t.kind)).toContain(
			'migration',
		);
		expect(buildTasks(sources({ inventory: old }), now).map((t) => t.kind)).not.toContain(
			'migration',
		);
	});

	it('marks a merge as syncing only while its own project still drifts', () => {
		const feed = [
			{ kind: 'merge', project: 'team-a', prNumber: 7, at: new Date(now).toISOString() },
		] as never;
		const drifting = buildTasks(
			sources({ feed, inventory: inv({ sync: 'OutOfSync' } as never) }),
			now,
		);
		const synced = buildTasks(sources({ feed, inventory: inv({}) }), now);
		expect(drifting.find((t) => t.kind === 'sync')?.status).toBe('ArgoCD syncing…');
		expect(synced.find((t) => t.kind === 'sync')?.status).toBe('Synced');
	});

	it('reports standing drift per out-of-sync VM', () => {
		const tasks = buildTasks(sources({ inventory: inv({ sync: 'OutOfSync' } as never) }), now);
		expect(tasks.filter((t) => t.kind === 'drift')).toHaveLength(1);
	});

	it('attributes staged and proposed rows to the caller', () => {
		const tasks = buildTasks(
			sources({
				drafts: [
					{
						project: 'team-a',
						draft: { items: [{ kind: 'edit', namespace: 'tenant-a', name: 'web' }] } as never,
					},
				],
				proposals: [{ project: 'team-a', prNumber: 3, prURL: 'u', title: '' }] as never,
			}),
			now,
		);
		expect(tasks.find((t) => t.kind === 'staged')?.by).toBe('alice');
		expect(tasks.find((t) => t.kind === 'pr')?.status).toBe('PR #3 open');
	});
});

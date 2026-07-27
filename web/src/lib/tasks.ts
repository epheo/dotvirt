import type { DraftView, Inventory, Proposal, TaskEntry } from './api';
import { duration } from './format';

// One row of the Recent Tasks feed.
export type Task = {
	kind: 'staged' | 'pr' | 'sync' | 'drift' | 'action' | 'migration';
	verb: string;
	namespace: string;
	name: string;
	prTitle: string;
	status: string;
	by: string;
	project: string;
	url: string;
	ok?: boolean; // for 'action'/'migration' rows: success
	at?: number; // for 'action' rows: timestamp (keeps keys unique)
	active?: boolean; // for 'migration' rows: still moving
};

export interface TaskSources {
	inventory: Inventory | null;
	feed: TaskEntry[];
	drafts: { project: string; draft: DraftView }[];
	proposals: Proposal[];
	username: string;
}

// Finished migrations linger briefly so an admin sees the outcome, not a
// vanishing row.
const migrationLingerMs = 15 * 60 * 1000;

// buildTasks folds the five sources into one feed ordered by lifecycle stage,
// not timestamp: live migrations, runtime ops, staged changes (the draft), open
// PRs (proposed), merged PRs, then standing drift (cluster differs from git).
// Pure over its inputs (now is injectable) so the trickiest frontend logic -
// the migration linger and the merge-to-reconcile "syncing" window - is
// unit-testable.
export function buildTasks(
	{ inventory, feed, drafts, proposals, username }: TaskSources,
	now = Date.now(),
): Task[] {
	const out: Task[] = [];
	// Live node-to-node moves (vCenter's vMotion rows), streamed off the VMI's
	// migration state.
	if (inventory) {
		for (const proj of inventory.projects)
			for (const ns of proj.namespaces)
				for (const vm of ns.vms) {
					const m = vm.migration;
					if (!m) continue;
					const active = !m.completed && !m.failed;
					const endedRecently = m.endedAt
						? now - new Date(m.endedAt).getTime() < migrationLingerMs
						: false;
					if (!active && !endedRecently) continue;
					out.push({
						kind: 'migration',
						verb: 'Live-migration',
						namespace: vm.namespace,
						name: vm.name,
						prTitle: '',
						status: active
							? `${m.sourceNode ?? '?'} → ${m.targetNode ?? '?'}${m.startedAt ? ` · ${duration(m.startedAt)}` : ''}`
							: m.failed
								? 'Failed'
								: `Migrated to ${m.targetNode ?? '?'}`,
						by: '—',
						project: proj.name,
						url: '',
						ok: !m.failed,
						active,
					});
				}
	}
	// Imperative runtime ops from the server feed (every admin's, not just this
	// browser's), most recent first with real attribution.
	for (const t of feed) {
		if (t.kind !== 'op') continue;
		out.push({
			kind: 'action',
			verb: t.verb,
			namespace: t.namespace ?? '',
			name: t.name ?? '',
			prTitle: '',
			status: t.ok ? 'Requested' : 'Failed',
			by: t.by ?? '—',
			project: t.project ?? '',
			url: '',
			ok: t.ok,
			at: Date.parse(t.at),
		});
	}
	for (const { project, draft } of drafts) {
		for (const it of draft.items) {
			out.push({
				kind: 'staged',
				verb: it.kind === 'edit' ? 'Reconfigure' : it.kind === 'create' ? 'Create' : 'Delete',
				namespace: it.namespace,
				name: it.name,
				prTitle: '',
				status: 'Staged',
				by: username,
				project,
				url: '',
			});
		}
	}
	for (const p of proposals) {
		out.push({
			kind: 'pr',
			verb: 'Proposed',
			namespace: '',
			name: '',
			prTitle: p.title || `PR #${p.prNumber}`,
			status: `PR #${p.prNumber} open`,
			by: username,
			project: p.project,
			url: p.prURL,
		});
	}
	// Merged PRs from the server feed (webhook-instant, forge-derived, so they
	// survive reloads and show every admin's merges): "syncing" while their
	// project still drifts - the merge-to-reconcile gap an admin can't
	// otherwise see.
	for (const t of feed) {
		if (t.kind !== 'merge') continue;
		const drifting = inventory?.projects.some(
			(p) =>
				p.name === t.project &&
				p.namespaces.some((ns) => ns.vms.some((v) => v.sync === 'OutOfSync')),
		);
		out.push({
			kind: 'sync',
			verb: 'Merged',
			namespace: '',
			name: '',
			prTitle: t.title || `PR #${t.prNumber}`,
			status: drifting ? 'ArgoCD syncing…' : 'Synced',
			by: t.by ?? '—',
			project: t.project ?? '',
			url: t.prURL ?? '',
			ok: true,
			active: !!drifting,
			at: Date.parse(t.at),
		});
	}
	if (inventory) {
		for (const proj of inventory.projects)
			for (const ns of proj.namespaces)
				for (const vm of ns.vms)
					if (vm.sync === 'OutOfSync')
						out.push({
							kind: 'drift',
							verb: 'Configuration drift',
							namespace: vm.namespace,
							name: vm.name,
							prTitle: '',
							status: 'Drifted',
							by: '—',
							project: proj.name,
							url: '',
						});
	}
	return out;
}

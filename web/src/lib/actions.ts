// The single VM-action registry: every menu that acts on a VM — the detail
// header's Actions ▾, the (planned) right-click context menu, the bulk bar —
// renders some projection of this list, so labels, ordering, and above all the
// enablement gates live exactly once.
//
// Two kinds of action:
//  - 'runtime': the registry runs it (an imperative, RBAC-gated API call that
//    doesn't touch git, so Argo never reverts it). Hosts wrap run() with their
//    own busy/result reporting; `verb` is the task-log wording.
//  - 'host': the embedding view performs it (open a modal, switch a tab,
//    download a file) — the registry only describes and gates it.
import { api, type VM } from '$lib/api';

export type ActionId =
	| 'restart'
	| 'pause'
	| 'unpause'
	| 'migrate'
	| 'console'
	| 'snapshot'
	| 'edit'
	| 'manifest'
	| 'delete';

export interface VMAction {
	id: ActionId;
	label: string;
	kind: 'runtime' | 'host';
	/** Task-log verb for runtime ops (e.g. "Live-migration requested"). */
	verb?: string;
	danger?: boolean;
	/** Draw a separator above this entry. */
	sep?: boolean;
	title?: string;
	enabled: (vm: VM) => boolean;
	run?: (vm: VM) => Promise<void>;
}

const running = (vm: VM) => vm.phase === 'Running';
const paused = (vm: VM) => !!vm.paused;
const always = () => true;

export const vmActions: VMAction[] = [
	{
		id: 'restart',
		label: 'Restart',
		kind: 'runtime',
		verb: 'Restart',
		enabled: running,
		run: (vm) => api.restart(vm.namespace, vm.name)
	},
	{
		id: 'pause',
		label: 'Pause',
		kind: 'runtime',
		verb: 'Pause',
		enabled: (vm) => running(vm) && !paused(vm),
		run: (vm) => api.pause(vm.namespace, vm.name)
	},
	{
		id: 'unpause',
		label: 'Unpause',
		kind: 'runtime',
		verb: 'Unpause',
		enabled: paused,
		run: (vm) => api.unpause(vm.namespace, vm.name)
	},
	{
		id: 'migrate',
		label: 'Live-migrate',
		kind: 'runtime',
		verb: 'Live-migration',
		enabled: running,
		run: (vm) => api.migrate(vm.namespace, vm.name)
	},
	{ id: 'console', label: 'Open console', kind: 'host', sep: true, enabled: running },
	{ id: 'snapshot', label: 'Snapshots', kind: 'host', enabled: always },
	{
		id: 'edit',
		label: 'Edit settings',
		kind: 'host',
		sep: true,
		title: 'Stages a config change into a PR',
		enabled: always
	},
	{
		id: 'manifest',
		label: 'Download manifest',
		kind: 'host',
		title: 'The VM definition as it exists in git',
		enabled: always
	},
	{
		id: 'delete',
		label: 'Delete VM',
		kind: 'host',
		danger: true,
		sep: true,
		title: 'Stages a removal into a PR',
		enabled: always
	}
];

/** The URL of a VM's manifest on the base branch — navigable (cookie-auth'd). */
export function manifestURL(vm: VM): string {
	return `/api/vms/${encodeURIComponent(vm.namespace)}/${encodeURIComponent(vm.name)}/manifest`;
}

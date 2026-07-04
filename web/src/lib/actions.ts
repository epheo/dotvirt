// The single VM-action registry: every menu that acts on a VM — the detail
// header's Actions ▾, the right-click context menu, the bulk bar — renders
// some projection of this list, so labels, ordering, and above all the
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
	| 'migrate-storage'
	| 'console'
	| 'snapshot'
	| 'clone'
	| 'template'
	| 'adopt'
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
// Git-backed verbs need a manifest on the base branch; a cluster-only VM (e.g.
// a fresh clone target) has none until adopted.
const inGit = (vm: VM) => !!vm.sourceFile;

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
		label: 'Live-migrate…',
		kind: 'host',
		title: 'Move the running VM to another host — pick a target or let the scheduler choose',
		enabled: running
	},
	{
		id: 'migrate-storage',
		label: 'Migrate storage…',
		kind: 'host',
		title: 'Live-copy disks to another storage class — staged as a PR',
		// Needs a live VMI to copy from, a git manifest to edit, and at least
		// one DataVolume-backed disk to move.
		enabled: (vm) => running(vm) && inGit(vm) && !!vm.disks?.some((d) => d.type === 'dataVolume')
	},
	{ id: 'console', label: 'Open console', kind: 'host', sep: true, enabled: running },
	{ id: 'snapshot', label: 'Snapshots', kind: 'host', enabled: always },
	{
		id: 'clone',
		label: 'Clone…',
		kind: 'host',
		title: 'Copy this VM via snapshot + restore; adopt the result into git after',
		enabled: always
	},
	{
		id: 'template',
		label: 'Clone to Template…',
		kind: 'host',
		title: 'Derive a reusable template from this VM’s git manifest — staged as a PR',
		enabled: inGit
	},
	{
		id: 'adopt',
		label: 'Adopt into git',
		kind: 'host',
		sep: true,
		title: 'Stage this cluster-only VM into a PR to bring it under GitOps',
		// The complement of inGit: only a NotTracked (live-but-ungitted) VM can be adopted.
		enabled: (vm) => vm.sync === 'NotTracked'
	},
	{
		id: 'edit',
		label: 'Edit settings',
		kind: 'host',
		title: 'Stages a config change into a PR',
		enabled: inGit
	},
	{
		id: 'manifest',
		label: 'Download manifest',
		kind: 'host',
		title: 'The VM definition as it exists in git',
		enabled: inGit
	},
	{
		id: 'delete',
		label: 'Delete VM',
		kind: 'host',
		danger: true,
		sep: true,
		title: 'Stages a removal into a PR',
		enabled: inGit
	}
];

/** The URL of a VM's manifest on the base branch — navigable (cookie-auth'd). */
export function manifestURL(vm: VM): string {
	return `/api/vms/${encodeURIComponent(vm.namespace)}/${encodeURIComponent(vm.name)}/manifest`;
}

/** A VM's console-screenshot PNG URL (cookie-auth'd); cb busts the img cache. */
export function screenshotURL(vm: VM, cb: number): string {
	return `/api/vms/${encodeURIComponent(vm.namespace)}/${encodeURIComponent(vm.name)}/screenshot?t=${cb}`;
}

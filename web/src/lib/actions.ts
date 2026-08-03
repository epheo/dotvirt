import { vmPath } from './api';
// The single VM-action registry: every menu that acts on a VM - the detail
// header's Actions ▾, the right-click context menu, the bulk bar - renders
// some projection of this list, so labels, ordering, and above all the
// enablement gates live exactly once.
//
// Two kinds of action:
//  - 'runtime': the registry runs it (an imperative, RBAC-gated API call that
//    doesn't touch git, so Argo never reverts it). Hosts wrap run() with their
//    own busy/result reporting; `verb` is the task-log wording.
//  - 'host': the embedding view performs it (open a modal, switch a tab,
//    download a file) - the registry only describes and gates it.
import { api, Unauthorized, type VM } from '$lib/api';
import { friendlyError } from '$lib/format';
import { ui } from '$lib/state/ui.svelte';

type ActionId =
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

// runRuntimeAction runs a registry runtime action with the standard toast
// feedback, so the wording cannot drift between the menus that trigger it.
export async function runRuntimeAction(a: VMAction, vm: VM): Promise<void> {
	const verb = a.verb ?? a.label;
	try {
		await a.run!(vm);
		ui.showToast(`${verb} requested for ${vm.name}.`, { kind: 'success' });
	} catch (e) {
		if (e instanceof Unauthorized) return; // signed out centrally; skip the error toast
		ui.showToast(`${verb} failed for ${vm.name}: ${friendlyError(e)}`, { kind: 'error' });
	}
}

// adoptVM stages a cluster-only VM's live state into the draft, with the one
// adopt toast, so wording cannot drift between the menus that trigger it.
// onstaged runs before the success toast so callers refresh their view first.
export async function adoptVM(
	vm: { namespace: string; name: string },
	opts?: { onstaged?: () => void | Promise<void> },
): Promise<void> {
	try {
		await api.adopt(vm.namespace, vm.name);
		await opts?.onstaged?.();
		ui.showToast(`${vm.name} staged into Changes - open a PR to adopt it into git.`, {
			kind: 'success',
			action: { label: 'Review & propose', run: () => (ui.changesOpen = true) },
		});
	} catch (e) {
		if (e instanceof Unauthorized) return; // signed out centrally; skip the error toast
		ui.showToast(friendlyError(e), { kind: 'error' });
	}
}

// adoptNamespaces stages everything the given namespaces run that git does not
// describe - VMs, disks, networks, policies - as one draft. Each namespace is
// adopted on its own: one that has nothing left to adopt (or that the caller may
// not read) must not abandon the rest half-done, with the already staged ones
// sitting in the draft unmentioned. Shared by the tree's context menu and the
// project-page banner, so wording and failure handling cannot drift.
// onstaged runs before the toast so callers refresh their view first.
export async function adoptNamespaces(
	namespaces: Iterable<string>,
	opts?: { onstaged?: () => void | Promise<void> },
): Promise<void> {
	const caveats: string[] = [];
	// The warning is project-wide and re-derived per call; keep only the last
	// instead of accumulating near-duplicates.
	let warning = '';
	let staged = 0;
	try {
		for (const ns of namespaces) {
			try {
				const view = await api.adoptNamespace(ns);
				staged++;
				// A capture bounded by the caller's RBAC (or by what ArgoCD still tracks) is
				// worth staging, but saying so matters: silence would read as "the namespace
				// is now fully described".
				warning = view.warning ?? '';
			} catch (e) {
				if (e instanceof Unauthorized) throw e;
				caveats.push(`${ns}: ${friendlyError(e)}`);
			}
		}
	} catch (e) {
		if (e instanceof Unauthorized) return;
	} finally {
		await opts?.onstaged?.();
	}
	const notes = [...caveats, warning].filter(Boolean).join(' ');
	if (!staged) {
		ui.showToast(notes || 'Nothing to adopt.', { kind: 'error' });
		return;
	}
	const action = { label: 'Review & propose', run: () => (ui.changesOpen = true) };
	if (notes) {
		ui.showToast(`Staged, but not everything: ${notes}`, { kind: 'warning', action });
	} else {
		ui.showToast('Staged into Changes - open a PR to adopt them into git.', {
			kind: 'success',
			action,
		});
	}
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
		run: (vm) => api.restart(vm.namespace, vm.name),
	},
	{
		id: 'pause',
		label: 'Pause',
		kind: 'runtime',
		verb: 'Pause',
		enabled: (vm) => running(vm) && !paused(vm),
		run: (vm) => api.pause(vm.namespace, vm.name),
	},
	{
		id: 'unpause',
		label: 'Unpause',
		kind: 'runtime',
		verb: 'Unpause',
		enabled: paused,
		run: (vm) => api.unpause(vm.namespace, vm.name),
	},
	{
		id: 'migrate',
		label: 'Live-migrate…',
		kind: 'host',
		title: 'Move the running VM to another host — pick a target or let the scheduler choose',
		enabled: running,
	},
	{
		id: 'migrate-storage',
		label: 'Migrate storage…',
		kind: 'host',
		title: 'Live-copy disks to another storage class — staged as a PR',
		// Needs a live VMI to copy from, a git manifest to edit, and at least
		// one DataVolume-backed disk to move.
		enabled: (vm) => running(vm) && inGit(vm) && !!vm.disks?.some((d) => d.type === 'dataVolume'),
	},
	{ id: 'console', label: 'Open console', kind: 'host', sep: true, enabled: running },
	{ id: 'snapshot', label: 'Snapshots', kind: 'host', enabled: always },
	{
		id: 'clone',
		label: 'Clone…',
		kind: 'host',
		title: 'Copy this VM via snapshot + restore; adopt the result into git after',
		enabled: always,
	},
	{
		id: 'template',
		label: 'Clone to Template…',
		kind: 'host',
		title: 'Derive a reusable template from this VM’s git manifest — staged as a PR',
		enabled: inGit,
	},
	{
		id: 'adopt',
		label: 'Adopt into git',
		kind: 'host',
		sep: true,
		title: 'Stage this cluster-only VM into a PR to bring it under GitOps',
		// The complement of inGit: only a NotTracked (live-but-ungitted) VM can be adopted.
		enabled: (vm) => vm.sync === 'NotTracked',
	},
	{
		id: 'edit',
		label: 'Edit settings',
		kind: 'host',
		title: 'Stages a config change into a PR',
		enabled: inGit,
	},
	{
		id: 'manifest',
		label: 'Download manifest',
		kind: 'host',
		title: 'The VM definition as it exists in git',
		enabled: inGit,
	},
	{
		id: 'delete',
		label: 'Delete VM',
		kind: 'host',
		danger: true,
		sep: true,
		title: 'Stages a removal into a PR',
		enabled: inGit,
	},
];

/** The URL of a VM's manifest on the base branch — navigable (cookie-auth'd). */
export function manifestURL(vm: VM): string {
	return `${vmPath(vm.namespace, vm.name)}/manifest`;
}

/** A VM's console-screenshot PNG URL (cookie-auth'd); cb busts the img cache. */
export function screenshotURL(vm: VM, cb: number): string {
	return `${vmPath(vm.namespace, vm.name)}/screenshot?t=${cb}`;
}

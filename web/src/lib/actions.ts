import { goto } from '$app/navigation';
import { vmPath, vmRef } from './api';
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
//    download a file) - the registry only describes and gates it. A
//    dialog-backed one names its modal in `open`, so no host has to keep an
//    id-to-dialog switch of its own.
import { api, Unauthorized, type DraftView, type VM } from '#lib/api.js';
import { friendlyError } from '#lib/format.js';
import { drafts } from '#lib/state/drafts.svelte.js';
import { templateSaveGate, vmGate, type Gate } from '#lib/gates.js';
import { ui, type AppModal } from '#lib/state/ui.svelte.js';
import { vmHref } from '#lib/nav.js';

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
	// Offered now: the VM's state allows it and so does the caller's right.
	enabled: (vm: VM) => boolean;
	// Why the caller may not, when that is what holds it back; '' otherwise.
	why?: (vm: VM) => string;
	run?: (vm: VM) => Promise<void>;
	/** The modal a dialog-backed host action opens. */
	open?: (vm: VM) => AppModal;
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

// dispatchVMAction performs a registry action from OUTSIDE the VM detail page
// (context menu, inspector, palette): runtime ops run here with toast feedback,
// host actions open their dialog or tab and land on the VM's page. Shared so
// the four-way split cannot drift between entry points.
export async function dispatchVMAction(a: VMAction, vm: VM): Promise<void> {
	if (a.kind === 'runtime' && a.run) {
		await runRuntimeAction(a, vm);
		return;
	}
	if (a.open) {
		ui.modal = a.open(vm);
		goto(vmHref(vm.namespace, vm.name));
		return;
	}
	switch (a.id) {
		case 'manifest':
			window.open(manifestURL(vm), '_blank');
			return;
		case 'adopt':
			await adoptVM(vm);
			return;
		case 'console':
			goto(vmHref(vm.namespace, vm.name, 'console'));
			return;
		case 'snapshot':
			goto(vmHref(vm.namespace, vm.name, 'snapshots'));
			return;
	}
}

// What a stage call acts on: a VM, or another draft resource by name.
type StageTarget = { resource?: string; namespace: string; name: string };

// Whether a stage call's answer still holds an entry for target. Staging a
// value git already holds unstages it, so the answer says what is staged,
// not the call.
export function isStaged(view: DraftView, target: StageTarget): boolean {
	const resource = target.resource || 'vm';
	return view.items.some(
		(i) =>
			(i.resource || 'vm') === resource &&
			i.namespace === target.namespace &&
			i.name === target.name,
	);
}

// toastStageResult reports a stage call by the draft view it returned: the
// staged toast while target keeps an entry, else that it matches git.
export function toastStageResult(view: DraftView, target: StageTarget, staged?: string): void {
	if (isStaged(view, target)) {
		ui.toastStaged(staged);
		return;
	}
	drafts.refresh();
	ui.showToast(`${target.name} already matches git; nothing is staged for it.`, { kind: 'info' });
}

// adoptVM stages a cluster-only VM's live state into the draft, with the one
// adopt toast, so wording cannot drift between the menus that trigger it.
export async function adoptVM(vm: { namespace: string; name: string }): Promise<void> {
	try {
		await api.adopt(vm.namespace, vm.name);
		ui.toastStaged(`${vm.name} staged into Changes - open a PR to adopt it into git.`);
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
export async function adoptNamespaces(namespaces: Iterable<string>): Promise<void> {
	const caveats: string[] = [];
	// The warning is project-wide and re-derived per call; keep only the last
	// instead of accumulating near-duplicates.
	let warning = '';
	let staged = 0;
	for (const ns of namespaces) {
		try {
			const view = await api.adoptNamespace(ns);
			staged++;
			// A capture bounded by the caller's RBAC (or by what ArgoCD still tracks) is
			// worth staging, but saying so matters: silence would read as "the namespace
			// is now fully described".
			warning = view.warning ?? '';
		} catch (e) {
			if (e instanceof Unauthorized) return; // signed out centrally; skip the toast
			caveats.push(`${ns}: ${friendlyError(e)}`);
		}
	}
	const notes = [...caveats, warning].filter(Boolean).join(' ');
	if (!staged) {
		ui.showToast(notes || 'Nothing to adopt.', { kind: 'error' });
		return;
	}
	if (notes) {
		ui.toastStaged(`Staged, but not everything: ${notes}`, { kind: 'warning' });
	} else {
		ui.toastStaged('Staged into Changes - open a PR to adopt them into git.');
	}
}

const running = (vm: VM) => vm.phase === 'Running';
const paused = (vm: VM) => !!vm.paused;
const always = () => true;
// Git-backed verbs need a manifest on the base branch; a cluster-only VM (e.g.
// a fresh clone target) has none until adopted.
const inGit = (vm: VM) => !!vm.sourceFile;

// gated folds the caller's right, read from the action's own capability field,
// into enabled, and keeps its reason for the host to show.
type Entry = VMAction & { gate?: (namespace: string) => Gate };
function gated({ gate, ...a }: Entry): VMAction {
	if (!gate) return a;
	const state = a.enabled;
	return {
		...a,
		enabled: (vm) => state(vm) && gate(vm.namespace).ok,
		why: (vm) => (state(vm) ? gate(vm.namespace).why : ''),
	};
}

const entries: Entry[] = [
	{
		id: 'restart',
		label: 'Restart',
		kind: 'runtime',
		verb: 'Restart',
		enabled: running,
		gate: vmGate.restart,
		run: (vm) => api.restart(vm.namespace, vm.name),
	},
	{
		id: 'pause',
		label: 'Pause',
		kind: 'runtime',
		verb: 'Pause',
		enabled: (vm) => running(vm) && !paused(vm),
		gate: vmGate.pause,
		run: (vm) => api.pause(vm.namespace, vm.name),
	},
	{
		id: 'unpause',
		label: 'Unpause',
		kind: 'runtime',
		verb: 'Unpause',
		enabled: paused,
		gate: vmGate.unpause,
		run: (vm) => api.unpause(vm.namespace, vm.name),
	},
	{
		id: 'migrate',
		label: 'Live-migrate…',
		kind: 'host',
		title: 'Move the running VM to another host — pick a target or let the scheduler choose',
		enabled: running,
		gate: vmGate.migrate,
		open: (vm) => ({ kind: 'migrateVM', vm: vmRef(vm) }),
	},
	{
		id: 'migrate-storage',
		label: 'Migrate storage…',
		kind: 'host',
		title: 'Live-copy disks to another storage class — staged as a PR',
		// Needs a live VMI to copy from, a git manifest to edit, and at least
		// one DataVolume-backed disk to move.
		enabled: (vm) => running(vm) && inGit(vm) && !!vm.disks?.some((d) => d.type === 'dataVolume'),
		gate: vmGate.update,
		open: (vm) => ({ kind: 'migrateStorage', vm: vmRef(vm) }),
	},
	{
		id: 'console',
		label: 'Open console',
		kind: 'host',
		sep: true,
		enabled: running,
		gate: vmGate.console,
	},
	{ id: 'snapshot', label: 'Snapshots', kind: 'host', enabled: always },
	{
		id: 'clone',
		label: 'Clone…',
		kind: 'host',
		title: 'Copy this VM via snapshot + restore; adopt the result into git after',
		enabled: always,
		gate: vmGate.clone,
		open: (vm) => ({ kind: 'cloneVM', vm: vmRef(vm) }),
	},
	{
		id: 'template',
		label: 'Clone to Template…',
		kind: 'host',
		title: 'Derive a reusable template from this VM’s git manifest — staged as a PR',
		enabled: inGit,
		gate: templateSaveGate,
		open: (vm) => ({ kind: 'saveTemplate', vm: vmRef(vm) }),
	},
	{
		id: 'adopt',
		label: 'Adopt into git',
		kind: 'host',
		sep: true,
		title: 'Stage this cluster-only VM into a PR to bring it under GitOps',
		// The complement of inGit: only a NotTracked (live-but-ungitted) VM can be adopted.
		enabled: (vm) => vm.sync === 'NotTracked',
		gate: vmGate.update,
	},
	{
		id: 'edit',
		label: 'Edit settings',
		kind: 'host',
		title: 'Stages a config change into a PR',
		enabled: inGit,
		gate: vmGate.update,
		open: (vm) => ({ kind: 'editVM', vm: vmRef(vm) }),
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
		gate: vmGate.delete,
		open: (vm) => ({ kind: 'deleteVM', vm: vmRef(vm) }),
	},
];

export const vmActions: VMAction[] = entries.map(gated);

/** The URL of a VM's manifest on the base branch — navigable (cookie-auth'd). */
export function manifestURL(vm: VM): string {
	return `${vmPath(vm.namespace, vm.name)}/manifest`;
}

/** A VM's console-screenshot PNG URL (cookie-auth'd); cb busts the img cache. */
export function screenshotURL(vm: VM, cb: number): string {
	return `${vmPath(vm.namespace, vm.name)}/screenshot?t=${cb}`;
}

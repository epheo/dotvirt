import { goto } from '$app/navigation';
import type {
	AdminNetworkPolicyCreate,
	EgressFirewallCreate,
	EgressIPCreate,
	ExternalRouteCreate,
	NetworkCreate,
	NetworkPolicyCreate,
	ProposeResult,
	Template,
	UplinkCreate,
	VM,
	VMRef,
} from '#lib/api.js';
import type { EditSection } from '#lib/editform.js';
import { reviewURL, type ReviewTarget } from '#lib/review.js';
import { drafts } from './drafts.svelte';
import { tray } from './tray.svelte';

export type EdgeInitial =
	{ kind: 'snat'; spec: EgressIPCreate } | { kind: 'route'; spec: ExternalRouteCreate };

// The new boot image form opened on a download: the source, empty when the
// caller picks it, else what imported disks downloaded (redacted: shown
// without the query string or credentials they carried), with their size.
export type ImageDownload = {
	kind: 'url' | 'registry';
	source: string;
	redacted?: boolean;
	size?: string;
	disks?: number;
};

// Every modal the shell can show, as one discriminated union - the shell
// renders exactly one, and opening any modal is a single assignment.
export type AppModal =
	// namespace on the create forms is the preselected target: the one the
	// opener's view is focused on, so a New from inside a project lands there.
	// image prefills the boot image, as "namespace/name". from names where the
	// prefill came from, finishing the sheet's "Filled in from ..." line
	// ("web-net, selected on the map"); without it the line names the namespace.
	| {
			kind: 'newVM';
			namespaces: string[] | null; // null = all creatable namespaces
			namespace?: string;
			image?: string;
			from?: string;
	  }
	// The network-family forms take an optional initial spec: the object read
	// back from git, so the create form doubles as the edit form. like is a
	// network's attach ref to fill a new one from (its project, its range).
	| {
			kind: 'newNetwork';
			initial?: NetworkCreate;
			namespace?: string;
			like?: string;
			from?: string;
	  }
	| { kind: 'uplink'; initial?: UplinkCreate }
	| { kind: 'namespace'; project: string | null }
	| { kind: 'newProject'; adopt?: string } // adopt = existing namespace to bring in as a project
	| { kind: 'adoptProject'; project: string; namespaces: string[]; recover?: boolean }
	| { kind: 'releaseProject'; project: string; namespaces: string[] }
	| { kind: 'syncAccess'; project: string }
	| { kind: 'publishing'; project: string }
	| { kind: 'egressFw'; namespaces: string[]; namespace?: string; initial?: EgressFirewallCreate }
	| { kind: 'projectRule'; namespaces: string[]; namespace?: string; initial?: NetworkPolicyCreate }
	| { kind: 'clusterEdge'; initial?: EdgeInitial }
	| { kind: 'adminFw'; initial?: AdminNetworkPolicyCreate }
	// Stage the removal of a git-declared object (network, firewall rule, cluster
	// edge object) - the VM delete's confirm, for the rest of the inventory.
	| { kind: 'deleteObject'; resource: string; namespace: string; name: string; sourceFile: string }
	| {
			kind: 'upload';
			namespace?: string;
			file?: File;
			bootImage?: boolean;
			download?: ImageDownload;
	  }
	| { kind: 'deployTemplate'; library?: string; template?: string; namespace?: string } // Deploy from Template (Templates tab, New menu)
	| { kind: 'editTemplate'; template: Template } // edit a library item's manifest (Templates tab)
	// Edit a declared object's manifest verbatim, when its form has no field for it.
	| {
			kind: 'editManifest';
			resource: string;
			namespace: string;
			name: string;
			sourceFile: string;
			yaml: string;
			baseBlob?: string; // the version yaml was read at; a stale edit is refused
			reason?: string;
	  }
	| { kind: 'staged'; vm: VMRef } // the per-VM staged-changes modal (from a Staged badge)
	| { kind: 'propose'; project: string } // review a project's draft and open its PR
	| { kind: 'undo'; project: string; hash: string } // revert a merged commit through a PR
	// The VM dialogs live here, not on the detail page, so every VM menu (detail
	// toolbar, context menu, inspector, palette) opens them the same way. They
	// hold the identity only: the host resolves the live VM every frame.
	| { kind: 'editVM'; vm: VMRef; section?: EditSection } // section = the step a Configure "Edit" jumps to
	| { kind: 'deleteVM'; vm: VMRef }
	| { kind: 'cloneVM'; vm: VMRef }
	| { kind: 'saveTemplate'; vm: VMRef }
	| { kind: 'migrateVM'; vm: VMRef }
	| { kind: 'migrateStorage'; vm: VMRef }
	// Check connectivity: the trace, from a VM when the opener names one.
	| { kind: 'trace'; source?: VMRef };

// Right-click context menus - the inventory's signature interaction. The bulk variant
// (right-click inside a grid multi-selection) renders inside the workspace that
// owns the selection; the shell renders the other two.
type CtxState =
	| { x: number; y: number; kind: 'vm'; vm: VM }
	| {
			x: number;
			y: number;
			kind: 'container';
			project: string;
			repo?: string;
			repoPage?: string;
			namespace?: string;
			namespaces: string[];
	  };

// warning is for an action that did what it could: it succeeded, but not wholly.
type ToastKind = 'success' | 'error' | 'warning' | 'info';
type Toast = {
	id: number;
	kind: ToastKind;
	msg: string;
	action?: { label: string; run: () => void };
};

class Ui {
	// Transient bottom-center toast stack (capped at 3); an optional action
	// renders as a button. One surfacing policy app-wide: imperative/runtime
	// results -> toast from every entry point; form submit + validation errors ->
	// inline in the modal that owns the form; ambient state (stream errors,
	// pending banners, live migrations) -> banners.
	toasts = $state<Toast[]>([]);
	#toastSeq = 0;
	showToast(msg: string, opts?: { kind?: ToastKind; action?: Toast['action'] }) {
		const t: Toast = {
			id: ++this.#toastSeq,
			kind: opts?.kind ?? 'info',
			msg,
			action: opts?.action,
		};
		this.toasts = [...this.toasts, t].slice(-3);
		// Errors linger longer - they carry the "what went wrong" the admin reads.
		setTimeout(() => this.dismissToast(t.id), t.kind === 'error' ? 8000 : 5000);
	}
	dismissToast(id: number) {
		this.toasts = this.toasts.filter((t) => t.id !== id);
	}
	// An arrow so it can be handed to action() unbound: the primitive takes a
	// reporter rather than importing the shell.
	toastError = (msg: string) => this.showToast(msg, { kind: 'error' });

	// A pull request opened or updated from the app: what happened, who acts
	// next, and the way to it.
	toastProposed(project: string, r: ProposeResult, what = 'PR') {
		const url = r.prURL || r.compareURL;
		this.showToast(
			r.prNumber
				? `${what} #${r.prNumber} ${r.existing ? 'updated' : 'opened'} in ${project}. Approve and merge it in the forge.`
				: `Pushed ${r.branch}. Open the pull request in the forge.`,
			{
				kind: 'success',
				action: url
					? {
							label: r.prURL ? `Open PR #${r.prNumber}` : 'Open in forge',
							run: () => window.open(url, '_blank', 'noopener'),
						}
					: undefined,
			},
		);
	}

	// The Changes section is a route (deep-linkable, back-button-walkable); this is
	// the one navigation every review affordance shares. A target lands on one
	// review (a staged item, a PR, a past commit) or a project's history.
	openChanges(target: ReviewTarget | null = null) {
		goto(reviewURL(target));
	}

	// What every successful stage does, wherever it was triggered (dialog,
	// banner, toolbar verb, batch): refresh the drafts summary and hand the user
	// the way forward. kind lets a partial batch keep that affordance while
	// reporting as a warning or error; target picks the review it opens.
	toastStaged(
		msg = 'Staged into Changes — applies when the project’s PR merges.',
		opts?: { kind?: ToastKind; target?: ReviewTarget },
	) {
		drafts.refresh();
		this.showToast(msg, {
			kind: opts?.kind ?? 'success',
			action: { label: 'Review & propose', run: () => this.reviewStaged(opts?.target) },
		});
	}

	// A staging toast's way forward: an explicit review target opens Changes on
	// it; otherwise the one project with a draft opens its propose sheet, and
	// several unfold the change tray to pick from.
	reviewStaged(target?: ReviewTarget) {
		if (target) return this.openChanges(target);
		const staged = drafts.drafts.filter((d) => d.draft.count > 0);
		if (staged.length === 1) this.modal = { kind: 'propose', project: staged[0].project };
		else tray.open = true;
	}

	modal = $state<AppModal | null>(null);
	ctx = $state<CtxState | null>(null);

	openVMContext(vm: VM, x: number, y: number) {
		this.ctx = { x, y, kind: 'vm', vm };
	}

	// A query for the masthead search box, pushed by a label chip on an object
	// page; the box takes it, focuses, and clears it.
	search = $state('');

	reset() {
		this.toasts = [];
		this.modal = null;
		this.ctx = null;
		this.search = '';
	}
}

export const ui = new Ui();

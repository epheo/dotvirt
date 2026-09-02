import { goto } from '$app/navigation';
import type { Template, VM } from '$lib/api';

// Every modal the shell can show, as one discriminated union - the shell
// renders exactly one, and opening any modal is a single assignment.
type AppModal =
	| { kind: 'newVM'; namespaces: string[] | null } // null = all creatable namespaces
	| { kind: 'newNetwork' }
	| { kind: 'uplink' }
	| { kind: 'namespace'; project: string | null }
	| { kind: 'newProject'; adopt?: string } // adopt = existing namespace to bring in as a project
	| { kind: 'adoptProject'; project: string; namespaces: string[]; recover?: boolean }
	| { kind: 'releaseProject'; project: string; namespaces: string[] }
	| { kind: 'egressFw'; namespaces: string[]; namespace?: string }
	| { kind: 'dfw'; namespaces: string[]; namespace?: string }
	| { kind: 'tier0' }
	| { kind: 'adminFw' }
	| { kind: 'upload' }
	| { kind: 'deployTemplate'; library?: string; template?: string } // Deploy from Template (Catalog / New ▾)
	| { kind: 'editTemplate'; template: Template } // edit a library item's manifest (Catalog)
	| { kind: 'staged'; vm: VM }; // the per-VM staged-changes modal (from a Staged badge)

// The host-kind registry actions the VM detail page fulfils with a modal or
// tab - what a context menu on an unopened VM may request it to open.
export type DetailAction =
	'edit' | 'delete' | 'console' | 'snapshot' | 'clone' | 'template' | 'migrate' | 'migrate-storage';

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

	// Review changes is a route (deep-linkable, back-button-walkable); this is
	// the one navigation every "Review & propose" affordance shares.
	openChanges() {
		goto('/changes');
	}

	modal = $state<AppModal | null>(null);
	ctx = $state<CtxState | null>(null);

	// A VM right-click inside the grid's multi-selection acts on the selection.
	// The workspace that owns `picked` registers this while mounted; it returns
	// true when it captured the event (and opened its own bulk menu).
	bulkIntercept: ((vm: VM, x: number, y: number) => boolean) | null = null;
	openVMContext(vm: VM, x: number, y: number) {
		if (this.bulkIntercept?.(vm, x, y)) return;
		this.ctx = { x, y, kind: 'vm', vm };
	}

	// A one-shot request for the VM page to open a modal/tab on arrival (context
	// menu -> "Edit settings" on an unopened VM); seq re-fires repeats.
	detailIntent = $state<{ id: DetailAction; seq: number } | null>(null);
	#intentSeq = 0;
	requestDetail(id: DetailAction) {
		this.detailIntent = { id, seq: ++this.#intentSeq };
	}

	// The masthead search instance, so object pages can push label queries into it.
	search: { searchFor: (q: string) => void } | null = null;

	reset() {
		this.toasts = [];
		this.modal = null;
		this.ctx = null;
		this.detailIntent = null;
	}
}

export const ui = new Ui();

import type { VM } from '$lib/api';

// Every modal the shell can show, as one discriminated union — the shell
// renders exactly one, and opening any modal is a single assignment.
export type AppModal =
	| { kind: 'newVM'; namespaces: string[] | null } // null = all creatable namespaces
	| { kind: 'newNetwork' }
	| { kind: 'uplink' }
	| { kind: 'namespace'; project: string | null }
	| { kind: 'newProject' }
	| { kind: 'adoptProject'; project: string; namespaces: string[] }
	| { kind: 'egressFw'; namespaces: string[]; namespace?: string }
	| { kind: 'dfw'; namespaces: string[]; namespace?: string }
	| { kind: 'tier0' }
	| { kind: 'adminFw' }
	| { kind: 'upload' }
	| { kind: 'staged'; vm: VM }; // the per-VM staged-changes modal (from a Staged badge)

// Right-click context menus — vCenter's signature interaction. The bulk variant
// (right-click inside a grid multi-selection) renders inside the workspace that
// owns the selection; the shell renders the other two.
export type CtxState =
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

class Ui {
	// Transient bottom-center toast; an optional action renders as a button.
	toast = $state<{ msg: string; action?: { label: string; run: () => void } } | null>(null);
	#toastTimer: ReturnType<typeof setTimeout> | undefined;
	showToast(msg: string, action?: { label: string; run: () => void }) {
		this.toast = { msg, action };
		clearTimeout(this.#toastTimer);
		this.#toastTimer = setTimeout(() => (this.toast = null), 5000);
	}

	// Runtime ops the user just triggered, surfaced in the dock's Recent Tasks.
	recentActions = $state<
		{ verb: string; namespace: string; name: string; ok: boolean; at: number }[]
	>([]);
	recordAction(a: { verb: string; namespace: string; name: string; ok: boolean }) {
		this.recentActions = [{ ...a, at: Date.now() }, ...this.recentActions].slice(0, 8);
	}

	// The Changes drawer — a summon-from-anywhere cart, deliberately not a route.
	changesOpen = $state(false);

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
	// menu → "Edit settings" on an unopened VM); seq re-fires repeats.
	detailIntent = $state<{
		id: 'edit' | 'delete' | 'console' | 'snapshot' | 'clone';
		seq: number;
	} | null>(null);
	#intentSeq = 0;
	requestDetail(id: 'edit' | 'delete' | 'console' | 'snapshot' | 'clone') {
		this.detailIntent = { id, seq: ++this.#intentSeq };
	}

	// The masthead search instance, so object pages can push label queries into it.
	search: { searchFor: (q: string) => void } | null = null;

	reset() {
		this.toast = null;
		this.recentActions = [];
		this.changesOpen = false;
		this.modal = null;
		this.ctx = null;
		this.detailIntent = null;
	}
}

export const ui = new Ui();

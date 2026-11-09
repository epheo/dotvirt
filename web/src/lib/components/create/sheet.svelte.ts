import { getContext, setContext } from 'svelte';
import type { CreateBodies, CreateKind } from '#lib/api.js';
import type { AppModal } from '#lib/state/ui.svelte.js';

export type SheetType = 'vm' | 'template' | 'image' | 'network' | 'rule' | 'outbound' | 'project';

// The chips, in board order, with what each one stages.
export const SHEET_TYPES: { id: SheetType; label: string; title: string }[] = [
	{ id: 'vm', label: 'VM', title: 'New VM' },
	{ id: 'template', label: 'VM from template', title: 'New VM from template' },
	{ id: 'image', label: 'Image', title: 'New image' },
	{ id: 'network', label: 'Network', title: 'New network' },
	{ id: 'rule', label: 'Firewall rule', title: 'New firewall rule' },
	{ id: 'outbound', label: 'Fixed outbound IP', title: 'New fixed outbound IP' },
	{ id: 'project', label: 'Project', title: 'New project' },
];

export type CreateModal = Extract<
	AppModal,
	{
		kind:
			| 'newVM'
			| 'deployTemplate'
			| 'upload'
			| 'newNetwork'
			| 'projectRule'
			| 'adminFw'
			| 'egressFw'
			| 'clusterEdge'
			| 'newProject';
	}
>;

const CREATE_KINDS = new Set<AppModal['kind']>([
	'newVM',
	'deployTemplate',
	'upload',
	'newNetwork',
	'projectRule',
	'adminFw',
	'egressFw',
	'clusterEdge',
	'newProject',
]);

export const isCreate = (m: AppModal | null): m is CreateModal => !!m && CREATE_KINDS.has(m.kind);

export function typeOf(m: CreateModal): SheetType {
	switch (m.kind) {
		case 'newVM':
			return 'vm';
		case 'deployTemplate':
			return 'template';
		case 'upload':
			return 'image';
		case 'newNetwork':
			return 'network';
		case 'projectRule':
		case 'adminFw':
		case 'egressFw':
			return 'rule';
		case 'clusterEdge':
			return 'outbound';
		case 'newProject':
			return 'project';
	}
}

// What the header's New opens: the type the section the user is in creates
// most, prefilled with the namespace in view.
export function newFromHere(
	path: string,
	namespace: string,
	namespaces: string[] | null,
): AppModal {
	const ns = namespace || undefined;
	if (path.startsWith('/networking')) {
		if (/[?&]tab=(security|firewall)/.test(path))
			return { kind: 'projectRule', namespaces: [], namespace: ns };
		return { kind: 'newNetwork', namespace: ns };
	}
	if (path.startsWith('/storage')) return { kind: 'upload', namespace: ns };
	if (/[?&]tab=templates/.test(path)) return { kind: 'deployTemplate', namespace: ns };
	return { kind: 'newVM', namespaces, namespace: ns };
}

// The repo path a create writes, mirroring the backend's renderers so the
// panel names the file before any dry run: a hint, the dry run is the truth.
export function stagePath<K extends CreateKind>(kind: K, body: CreateBodies[K]): string {
	const b = body as unknown as Record<string, unknown>;
	const ns = String(b.namespace ?? '');
	const name = String(b.name || '<name>');
	switch (kind) {
		case 'vm':
		case 'template':
			return `${ns}/${b.name || '<generated>'}.yaml`;
		case 'network':
			return (b.scope ?? 'project') === 'project'
				? `${ns}/networks/${name}.yaml`
				: `networks/${name}.yaml`;
		case 'networkPolicy':
			return `${ns}/networkpolicies/${name}.yaml`;
		case 'egressFirewall':
			return `${ns}/egressfirewalls/default.yaml`;
		case 'adminNetworkPolicy':
			return b.baseline
				? 'baselineadminnetworkpolicies/default.yaml'
				: `adminnetworkpolicies/${name}.yaml`;
		case 'egressIP':
			return `egressips/${name}.yaml`;
		case 'externalRoute':
			return `externalroutes/${name}.yaml`;
		case 'project':
			return `namespaces/${String(b.namespace || name)}.yaml`;
		default:
			return '';
	}
}

// The sheet's link to the form inside it: the form registers its submit for
// Ctrl+Enter, and pins the sheet while closing would lose work in flight.
export class SheetCtl {
	pinned = $state(false);
	submit: (() => void) | null = null;
}

const KEY = Symbol('create-sheet');
export const provideSheet = (c: SheetCtl) => setContext(KEY, c);
export const useSheet = () => getContext<SheetCtl>(KEY);

// The board's form controls: 36px fields, radius 6, the strong hairline.
export const FIELD =
	'h-9 w-full min-w-0 rounded-md border border-line-strong bg-panel px-2.5 text-sm text-ink placeholder:text-ink-faint focus:border-accent focus:outline-none focus:ring-1 focus:ring-accent disabled:bg-inset-strong disabled:text-ink-faint';
export const LABEL = 'text-[13px] leading-5 font-medium text-ink';
export const HELP = 'flex items-center gap-1.5 text-xs leading-4 text-ink-faint';
// The compact control a rule sentence is built from.
export const SMALL =
	'h-8 min-w-0 rounded-md border border-line-strong bg-panel px-2 text-[13px] text-ink placeholder:text-ink-faint focus:border-accent focus:outline-none focus:ring-1 focus:ring-accent disabled:bg-inset-strong disabled:text-ink-faint';

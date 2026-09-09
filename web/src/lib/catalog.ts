import type { Options, Template } from './api';

// The Catalog's kinds and the row each kind lists. One model feeds both the
// tree (titles) and the workspace (table + detail), so they can't disagree.
export type CatalogKind =
	'templates' | 'images' | 'instancetypes' | 'preferences' | 'networks' | 'storage';

export const CATALOG_KINDS: { id: CatalogKind; label: string }[] = [
	{ id: 'templates', label: 'VM Templates' },
	{ id: 'images', label: 'Boot images' },
	{ id: 'instancetypes', label: 'Instance types' },
	{ id: 'preferences', label: 'Preferences' },
	{ id: 'networks', label: 'Networks' },
	{ id: 'storage', label: 'Storage classes' },
];

export const catalogKind = (v: string | null): CatalogKind =>
	CATALOG_KINDS.some((k) => k.id === v) ? (v as CatalogKind) : 'templates';

export const catalogHref = (kind: CatalogKind, item?: string): string =>
	`/catalog?kind=${kind}${item ? `&item=${encodeURIComponent(item)}` : ''}`;

// The shared library reads as a subscribed content library.
export const libraryLabel = (lib: string) => (lib === 'platform' ? 'Shared library' : lib);

export type CatalogRow = {
	key: string;
	title: string;
	fact: string;
	detail: [string, string][];
	template?: Template; // template rows carry the object for parameters + Deploy
};

// null while the kind's source hasn't arrived, so callers can tell loading
// from empty.
export function catalogRows(
	kind: CatalogKind,
	templates: Template[] | null,
	options: Options | null,
): CatalogRow[] | null {
	if (kind === 'templates') {
		if (!templates) return null;
		return templates.map((t) => ({
			key: `${t.library}/${t.name}`,
			title: t.name,
			fact: t.error
				? 'Invalid'
				: [libraryLabel(t.library), t.instancetype].filter(Boolean).join(' · '),
			detail: [
				['Kind', 'VirtualMachineTemplate (git)'],
				['Library', libraryLabel(t.library)],
				['Description', t.description || '—'],
				['Instance type', t.instancetype || '—'],
				['Preference', t.preference || '—'],
				['Source file', t.sourceFile],
				...(t.error ? ([['Error', t.error]] as [string, string][]) : []),
			],
			template: t,
		}));
	}
	const o = options;
	if (!o) return null;
	switch (kind) {
		case 'images':
			return (o.osImages ?? []).map((i) => ({
				key: `${i.namespace}/${i.name}`,
				title: i.name,
				fact: i.ready ? 'Ready' : 'Not ready',
				detail: [
					['Kind', 'DataSource (CDI)'],
					['Namespace', i.namespace],
					['Ready', i.ready ? 'Yes' : 'No'],
					['Used as', 'Root-disk source in the New VM wizard'],
				],
			}));
		case 'instancetypes':
			return (o.instancetypes ?? []).map((it) => ({
				key: it.name,
				title: it.name,
				fact: `${it.cpu} CPU / ${it.memory}`,
				detail: [
					['Kind', 'VirtualMachineClusterInstancetype'],
					['vCPUs', String(it.cpu)],
					['Memory', it.memory],
					['Used as', 'VM size (spec.instancetype)'],
				],
			}));
		case 'preferences':
			return (o.preferences ?? []).map((p) => ({
				key: p.name,
				title: p.displayName || p.name,
				fact: p.name,
				detail: [
					['Kind', 'VirtualMachineClusterPreference'],
					['Name', p.name],
					['Display name', p.displayName || '—'],
					['Used as', 'OS tuning (spec.preference)'],
				],
			}));
		case 'networks':
			return (o.networks ?? []).map((n) => ({
				key: `${n.namespace}/${n.name}`,
				title: n.name,
				fact: n.namespace,
				detail: [
					['Kind', 'NetworkAttachmentDefinition (Multus)'],
					['Namespace', n.namespace],
					['Reference', `${n.namespace}/${n.name}`],
					['Used as', 'Secondary VM network'],
				],
			}));
		case 'storage':
			return (o.storageClasses ?? []).map((sc) => ({
				key: sc.name,
				title: sc.name,
				fact: sc.default ? 'default' : '',
				detail: [
					['Kind', 'StorageClass'],
					['Cluster default', sc.default ? 'Yes' : 'No'],
					['Used as', 'dataVolume storage class for provisioned disks'],
				],
			}));
	}
}

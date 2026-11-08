import type { Network, OSImage, Options, StorageClass, Template } from './api';
import { plural, quantity } from './format';
import { networkKey, networkKind } from './networks';

// The Catalog's kinds and the row each kind lists. One model feeds the tree,
// the tab's table and the open item's panel, so they cannot disagree.
export type CatalogKind =
	'templates' | 'images' | 'instancetypes' | 'preferences' | 'networks' | 'storage';

export const CATALOG_KINDS: { id: CatalogKind; label: string }[] = [
	{ id: 'templates', label: 'VM templates' },
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

// The platform repo's library, offered to every project.
export const libraryLabel = (lib: string) => (lib === 'platform' ? 'Shared library' : lib);

export type CatalogRow = {
	key: string;
	title: string; // the object's name
	fact: string; // the table's muted detail
	about: string; // the panel's line under the name
	detail: Detail[];
	template?: Template;
	image?: OSImage;
	network?: { known?: Network; namespaces: string[] };
	storageClass?: StorageClass;
};

// A fact in the open item's panel; name marks a value that is an object or
// file name, set in mono.
type Detail = [label: string, value: string, name?: boolean];
const named = (label: string, value = ''): Detail => [label, value || '-', !!value];

const size = (cpu: number, memory: string) => `${cpu} vCPU, ${quantity(memory)}`;

// The network an attachment belongs to: a project network by its exact
// attach ref, a shared one by name where it is published to that namespace.
function owners(networks: Network[]) {
	const byRef = new Map(networks.filter((n) => n.attachRef).map((n) => [n.attachRef!, n]));
	const shared = new Map(networks.filter((n) => n.scope === 'shared').map((n) => [n.name, n]));
	return (namespace: string, name: string): Network | undefined => {
		const exact = byRef.get(`${namespace}/${name}`);
		if (exact) return exact;
		const n = shared.get(name);
		return n && (!n.namespaces?.length || n.namespaces.includes(namespace)) ? n : undefined;
	};
}

const some = (names: string[]) =>
	names.length > 3
		? `${names.slice(0, 3).join(', ')} and ${names.length - 3} more`
		: names.join(', ');

// Attachments group by the network they belong to: a shared network has one
// in each namespace it is published to, and reads as one row. An attachment
// no known network owns keeps a row of its own.
function networkRows(options: Options, networks: Network[]): CatalogRow[] {
	const owner = owners(networks);
	const rows = new Map<string, { title: string; known?: Network; namespaces: string[] }>();
	for (const a of options.networks ?? []) {
		const known = owner(a.namespace, a.name);
		const key = known ? networkKey(known) : `${a.namespace}/${a.name}`;
		const row = rows.get(key) ?? { title: a.name, known, namespaces: [] };
		row.namespaces.push(a.namespace);
		rows.set(key, row);
	}
	return [...rows].map(([key, { title, known, namespaces }]) => {
		const what = known ? networkKind(known) : 'Network attachment';
		const where =
			namespaces.length === 1
				? `in ${namespaces[0]}`
				: `in ${plural(namespaces.length, 'namespace')}`;
		return {
			key,
			title,
			fact: `${what}, ${where}`,
			about: `${what} ${where}`,
			detail: [
				named('Kind', known ? known.backing : 'NetworkAttachmentDefinition'),
				namespaces.length === 1
					? named('Namespace', namespaces[0])
					: ['Namespaces', some(namespaces)],
				named('Attach as', known?.attachRef ?? `${namespaces[0]}/${title}`),
				['Used as', 'A second VM network'],
			],
			network: { known, namespaces },
		};
	});
}

// null while the kind's source hasn't arrived, so callers can tell loading
// from empty. networks are the live ones attachments are grouped under.
export function catalogRows(
	kind: CatalogKind,
	templates: Template[] | null,
	options: Options | null,
	networks: Network[] = [],
): CatalogRow[] | null {
	if (kind === 'templates') {
		if (!templates) return null;
		return templates.map((t) => ({
			key: `${t.library}/${t.name}`,
			title: t.name,
			fact: libraryLabel(t.library),
			about: `VM template in ${libraryLabel(t.library)}${t.error ? '. It does not parse' : ''}`,
			detail: [
				['Library', libraryLabel(t.library)],
				...(t.description ? [['Description', t.description] as Detail] : []),
				named('Instance type', t.instancetype),
				named('Preference', t.preference),
				named('Boot image', t.image),
				named('File', t.sourceFile),
				...(t.error ? [['Error', t.error] as Detail] : []),
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
				fact: i.ready ? 'Ready' : 'Importing',
				about: `Boot image in ${i.namespace}. ${i.ready ? 'Ready' : 'Importing'}`,
				detail: [
					named('Kind', 'DataSource'),
					named('Namespace', i.namespace),
					['Used as', 'Root-disk source of a new VM'],
				],
				image: i,
			}));
		case 'instancetypes':
			return (o.instancetypes ?? []).map((it) => ({
				key: it.name,
				title: it.name,
				fact: size(it.cpu, it.memory),
				about: 'Instance type, cluster-wide',
				detail: [
					named('Kind', 'VirtualMachineClusterInstancetype'),
					['vCPU', String(it.cpu)],
					['Memory', quantity(it.memory)],
					['Used as', 'VM size (spec.instancetype)'],
				],
			}));
		case 'preferences':
			return (o.preferences ?? []).map((p) => {
				const least = [p.minCPU ? `${p.minCPU} vCPU` : '', p.minMemory ? quantity(p.minMemory) : '']
					.filter(Boolean)
					.join(', ');
				return {
					key: p.name,
					title: p.name,
					fact:
						[p.displayName, least ? `needs ${least}` : ''].filter(Boolean).join(', ') ||
						'No minimum size',
					about: 'Preference, cluster-wide',
					detail: [
						named('Kind', 'VirtualMachineClusterPreference'),
						['Display name', p.displayName || '-'],
						['Needs at least', least || '-'],
						['Used as', 'OS tuning (spec.preference)'],
					],
				};
			});
		case 'networks':
			return networkRows(o, networks);
		case 'storage':
			return (o.storageClasses ?? []).map((sc) => ({
				key: sc.name,
				title: sc.name,
				fact: sc.default ? 'Cluster default' : '-',
				about: sc.default ? 'Storage class, the cluster default' : 'Storage class',
				detail: [
					named('Kind', 'StorageClass'),
					['Cluster default', sc.default ? 'Yes' : 'No'],
					['Used as', 'Where a new disk is provisioned'],
				],
				storageClass: sc,
			}));
	}
}

// The header's muted line: what new VMs are made from, counted. A source
// still loading is left out rather than counted as none.
export function catalogContext(templates: Template[] | null, options: Options | null): string {
	const parts: string[] = [];
	if (templates) {
		const libraries = new Set(templates.map((t) => t.library)).size;
		parts.push(
			libraries > 1
				? `${plural(templates.length, 'template')} in ${plural(libraries, 'library', 'libraries')}`
				: plural(templates.length, 'template'),
		);
	}
	if (options) {
		parts.push(plural(options.osImages?.length ?? 0, 'boot image'));
		parts.push(plural(options.instancetypes?.length ?? 0, 'instance type'));
	}
	return parts.join(', ');
}

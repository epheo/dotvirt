// URL scheme: every view is a route, so views are deep-linkable and the back
// button walks objects. Tabs ride ?tab= with replaceState (back never walks
// tab flips). The VM route is section-agnostic - every section's tree opens VMs.
import type { Scope } from './lenses';

export type Section = 'compute' | 'hosts' | 'networking' | 'storage' | 'changes';

// The inventory sections: where "/" may land, and what carries an object tree
// of its own. Changes is a section too, but never a home.
export const INVENTORY_SECTIONS: Section[] = ['compute', 'hosts', 'networking', 'storage'];

const enc = encodeURIComponent;

export function hrefForScope(s: Scope): string {
	switch (s.kind) {
		case 'all':
			return '/compute';
		case 'project':
			return `/compute/${enc(s.project)}`;
		case 'namespace':
			return `/compute/${enc(s.project)}/${enc(s.namespace)}`;
		case 'node':
			return `/hosts/${enc(s.node)}`;
		case 'network':
			// Network keys may carry a raw "ns/name" NAD ref - the route is a rest
			// param, so the slash stays a slash.
			return `/networking/${s.network.split('/').map(enc).join('/')}`;
		case 'storage':
			return `/storage/${enc(s.storageClass)}`;
	}
}

/** A network's page, by its key (see networks.networkKey). */
export function networkHref(key: string, tab?: string): string {
	return `${hrefForScope({ kind: 'network', network: key })}${tab ? `?tab=${tab}` : ''}`;
}

export function vmHref(namespace: string, name: string, tab?: string): string {
	return `/vm/${enc(namespace)}/${enc(name)}${tab ? `?tab=${tab}` : ''}`;
}

// The section a path belongs to - drives the tree's lens and the section
// highlight. null for the VM route, which keeps whichever tree opened it.
export function sectionOf(pathname: string): Section | null {
	const head = pathname.split('/')[1];
	switch (head) {
		case 'compute':
		case 'hosts':
		case 'networking':
		case 'storage':
		case 'changes':
			return head;
	}
	return null;
}

// The Changes section's scope routes: the whole inventory, one project, one
// namespace. Selection (a staged item, a PR, a commit) rides the query, see
// review.ts.
export function changesHref(project?: string, namespace?: string): string {
	if (!project) return '/changes';
	return `/changes/${enc(project)}${namespace ? `/${enc(namespace)}` : ''}`;
}

// The Changes section's tabs, one job each: what waits to be proposed, what
// waits in the forge, what already landed. Every scope (all, a project, a
// namespace) carries all three.
export const CHANGES_TABS = [
	{ id: 'staged', label: 'Staged' },
	{ id: 'review', label: 'In review' },
	{ id: 'history', label: 'History' },
] as const;
export type ChangesTab = (typeof CHANGES_TABS)[number]['id'];

// null when ?tab= names no Changes tab: the page then picks one from the
// selection and from what waits.
export const changesTab = (v: string | null): ChangesTab | null =>
	CHANGES_TABS.find((t) => t.id === v)?.id ?? null;

export const sectionRoot = (s: Section): string => `/${s}`;

/** Compute's Templates tab, with one template open when item names it. */
export const templatesHref = (item?: string): string =>
	`/compute?tab=templates${item ? `&item=${enc(item)}` : ''}`;

// The VM detail tabs: rendered by the page, validated by the route guard and
// by keepTab.
export const VM_TABS = [
	{ id: 'summary', label: 'Summary' },
	{ id: 'monitor', label: 'Monitor' },
	{ id: 'configure', label: 'Configure' },
	{ id: 'security', label: 'Security' },
	{ id: 'permissions', label: 'Permissions' },
	{ id: 'changes', label: 'Changes' },
	{ id: 'snapshots', label: 'Snapshots' },
	{ id: 'console', label: 'Console' },
] as const;
export type VMTab = (typeof VM_TABS)[number]['id'];

// The Networking root's tabs and one network's page tabs.
export const NETWORKING_TABS = [
	{ id: 'map', label: 'Map' },
	{ id: 'networks', label: 'Networks' },
	{ id: 'firewall', label: 'Firewall' },
	{ id: 'uplinks', label: 'Uplinks' },
	{ id: 'vms', label: 'VMs' },
];
export const NETWORK_TABS = [
	{ id: 'summary', label: 'Summary' },
	{ id: 'vms', label: 'VMs' },
	{ id: 'rules', label: 'Rules' },
	{ id: 'history', label: 'History' },
];

// The Networking root's tab for a ?tab= value: the old names still deep-link
// (its Summary was the topology, its Security the policy plane).
export function networkingTab(raw: string | null, tabs = NETWORKING_TABS): string {
	if (raw === 'security') return 'firewall';
	return tabs.some((t) => t.id === raw) ? raw! : 'map';
}

// The Storage root's tabs: one section page, each tab one job. Disks, Images
// and Snapshots are where the Library's counts drill in.
export const STORAGE_TABS = [
	{ id: 'library', label: 'Library' },
	{ id: 'disks', label: 'Disks' },
	{ id: 'images', label: 'Images' },
	{ id: 'snapshots', label: 'Snapshots' },
	{ id: 'classes', label: 'Classes' },
] as const;
export type StorageTab = (typeof STORAGE_TABS)[number]['id'];

export const storageTab = (
	v: string | null,
	tabs: readonly { id: StorageTab }[] = STORAGE_TABS,
): StorageTab => tabs.find((t) => t.id === v)?.id ?? tabs[0].id;

// A Storage root view: the tab plus its query (project scope, disk filters,
// the open disk). The tab is always explicit so the breadcrumb and the tree,
// which keep the current tab on plain hrefs, land exactly here.
export function storageHref(
	tab: StorageTab,
	params: Record<string, string | undefined> = {},
): string {
	const q = new URLSearchParams({ tab });
	for (const [k, v] of Object.entries(params)) if (v) q.set(k, v);
	return `/storage?${q}`;
}

const CONTAINER_TABS = [
	{ id: 'summary', label: 'Summary' },
	{ id: 'vms', label: 'VMs' },
	{ id: 'templates', label: 'Templates' },
	{ id: 'monitor', label: 'Monitor' },
	{ id: 'configure', label: 'Configure' },
	{ id: 'security', label: 'Security' },
	{ id: 'permissions', label: 'Permissions' },
];

// The tab set of a container: compute containers carry the full set, hosts
// drop Permissions (nodes aren't namespaced) and configure rebalancing at the root,
// Networking has its own (map and firewall at the root, a network's rules and
// history on its page). Storage has its own tabs (STORAGE_TABS).
export function containerTabs(scope: Scope, section: Section): typeof CONTAINER_TABS {
	const only = (...ids: string[]) => CONTAINER_TABS.filter((t) => ids.includes(t.id));
	switch (section) {
		case 'hosts':
			return only('summary', 'vms', 'monitor', 'configure');
		case 'networking':
			return scope.kind === 'all' ? NETWORKING_TABS : NETWORK_TABS;
	}
	// Effective policy evaluates against exactly one namespace; a template
	// library belongs to a project.
	return CONTAINER_TABS.filter(
		(t) => t.id !== (scope.kind === 'namespace' ? 'templates' : 'security'),
	);
}

// The tabs an href lands on; null for anything else.
function tabsAt(href: string): string[] | null {
	const path = href.split('?')[0];
	if (path.split('/')[1] === 'vm') return VM_TABS.map((t) => t.id);
	const section = sectionOf(path);
	if (section === 'changes') return CHANGES_TABS.map((t) => t.id);
	if (!section) return null;
	const scope = scopeFromPath(path);
	// A class drill-in is one page without tabs.
	if (section === 'storage') return scope.kind === 'all' ? STORAGE_TABS.map((t) => t.id) : [];
	return containerTabs(scope, section).map((t) => t.id);
}

// Moving between objects keeps the tab in view when the target has it: a
// Monitor-to-Monitor walk down the tree, VM to VM on Console. Summary is the
// default already, and an unsupported tab would only fall back to it.
export function keepTab(href: string, tab: string | null): string {
	if (!tab || tab === 'summary' || href.includes('?')) return href;
	return tabsAt(href)?.includes(tab) ? `${href}?tab=${tab}` : href;
}

// The workspace breadcrumb for a scope: ancestors link, the focus is plain.
// Roots name the section, not "All VMs" everywhere.
export function trailForScope(s: Scope, root = 'All VMs'): { label: string; href?: string }[] {
	switch (s.kind) {
		case 'all':
			return [{ label: root }];
		case 'project':
			return [{ label: root, href: '/compute' }, { label: s.project }];
		case 'namespace':
			return [
				{ label: root, href: '/compute' },
				{ label: s.project, href: hrefForScope({ kind: 'project', project: s.project }) },
				{ label: s.namespace },
			];
		case 'node':
			return [{ label: 'Hosts', href: '/hosts' }, { label: s.node }];
		case 'network':
			return [{ label: 'Networking', href: '/networking' }, { label: s.network }];
		case 'storage':
			return [
				{ label: 'Storage', href: storageHref('library') },
				{ label: 'Classes', href: storageHref('classes') },
				{ label: s.storageClass },
			];
	}
}

// The inverse of hrefForScope: the Scope a path focuses (the section roots and
// non-scope routes like /vm read as 'all').
export function scopeFromPath(pathname: string): Scope {
	const parts = pathname.split('/').slice(1).map(decodeURIComponent);
	switch (parts[0]) {
		case 'compute':
			if (parts.length >= 3) return { kind: 'namespace', project: parts[1], namespace: parts[2] };
			if (parts.length === 2) return { kind: 'project', project: parts[1] };
			return { kind: 'all' };
		case 'hosts':
			return parts.length >= 2 ? { kind: 'node', node: parts[1] } : { kind: 'all' };
		case 'networking':
			return parts.length >= 2
				? { kind: 'network', network: parts.slice(1).join('/') }
				: { kind: 'all' };
		case 'storage':
			return parts.length >= 2 ? { kind: 'storage', storageClass: parts[1] } : { kind: 'all' };
	}
	return { kind: 'all' };
}

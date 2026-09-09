// URL scheme: every view is a route, so views are deep-linkable and the back
// button walks objects. Tabs ride ?tab= with replaceState (back never walks
// tab flips). The VM route is section-agnostic - every section's tree opens VMs.
import type { Scope } from './lenses';

export type Section = 'compute' | 'hosts' | 'networking' | 'storage' | 'catalog' | 'changes';

// The inventory sections: where "/" may land, and what carries an object tree
// of its own. Changes is a section too, but never a home.
export const INVENTORY_SECTIONS: Section[] = [
	'compute',
	'hosts',
	'networking',
	'storage',
	'catalog',
];

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
		case 'catalog':
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

export const sectionRoot = (s: Section): string => `/${s}`;

// The VM detail tabs, validated by the route guard and by keepTab.
export const VM_TABS = [
	'summary',
	'monitor',
	'configure',
	'security',
	'permissions',
	'changes',
	'snapshots',
	'console',
] as const;
export type VMTab = (typeof VM_TABS)[number];

export const CONTAINER_TABS = [
	{ id: 'summary', label: 'Summary' },
	{ id: 'vms', label: 'VMs' },
	{ id: 'monitor', label: 'Monitor' },
	{ id: 'configure', label: 'Configure' },
	{ id: 'security', label: 'Security' },
	{ id: 'permissions', label: 'Permissions' },
];

// The tab set of a container: compute containers carry the full set, hosts
// drop Permissions (nodes aren't namespaced) and configure DRS at the root,
// networking's root carries the policy plane as Security, segments and
// storage classes are fact sheets (Summary + their VMs).
export function containerTabs(scope: Scope, section: Section): typeof CONTAINER_TABS {
	const only = (...ids: string[]) => CONTAINER_TABS.filter((t) => ids.includes(t.id));
	switch (section) {
		case 'hosts':
			return only('summary', 'vms', 'monitor', 'configure');
		case 'networking':
			return scope.kind === 'all' ? only('summary', 'vms', 'security') : only('summary', 'vms');
		case 'storage':
			return only('summary', 'vms');
	}
	// Effective policy evaluates against exactly one namespace.
	return scope.kind === 'namespace'
		? CONTAINER_TABS
		: CONTAINER_TABS.filter((t) => t.id !== 'security');
}

// The tabs an inventory href lands on; null for anything else (catalog, changes).
function tabsAt(href: string): string[] | null {
	const path = href.split('?')[0];
	if (path.split('/')[1] === 'vm') return [...VM_TABS];
	const section = sectionOf(path);
	if (!section || section === 'catalog' || section === 'changes') return null;
	return containerTabs(scopeFromPath(path), section).map((t) => t.id);
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
export function trailForScope(s: Scope): { label: string; href?: string }[] {
	switch (s.kind) {
		case 'all':
			return [{ label: 'All VMs' }];
		case 'project':
			return [{ label: 'All VMs', href: '/compute' }, { label: s.project }];
		case 'namespace':
			return [
				{ label: 'All VMs', href: '/compute' },
				{ label: s.project, href: hrefForScope({ kind: 'project', project: s.project }) },
				{ label: s.namespace },
			];
		case 'node':
			return [{ label: 'All Nodes', href: '/hosts' }, { label: `Node: ${s.node}` }];
		case 'network':
			return [{ label: 'All Networks', href: '/networking' }, { label: `Segment: ${s.network}` }];
		case 'storage':
			return [{ label: 'All Storage', href: '/storage' }, { label: `Storage: ${s.storageClass}` }];
	}
}

// The inverse of hrefForScope: the Scope a path focuses (the section roots and
// non-scope routes - /vm, /catalog - read as 'all').
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

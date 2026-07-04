// URL scheme: every view is a route, so views are deep-linkable and the back
// button walks objects. Tabs ride ?tab= with replaceState (back never walks
// tab flips). The VM route is section-agnostic — every section's tree opens VMs.
import type { Scope } from './lenses';

export type Section = 'compute' | 'hosts' | 'networking' | 'storage' | 'catalog';

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
			// Network keys may carry a raw "ns/name" NAD ref — the route is a rest
			// param, so the slash stays a slash.
			return `/networking/${s.network.split('/').map(enc).join('/')}`;
		case 'storage':
			return `/storage/${enc(s.storageClass)}`;
	}
}

export function vmHref(namespace: string, name: string, tab?: string): string {
	return `/vm/${enc(namespace)}/${enc(name)}${tab ? `?tab=${tab}` : ''}`;
}

// The inventory section a path belongs to — drives the tree's lens and the
// section highlight. The VM route keeps the Compute tree.
export function sectionOf(pathname: string): Section {
	const head = pathname.split('/')[1];
	if (head === 'hosts' || head === 'networking' || head === 'storage' || head === 'catalog')
		return head;
	return 'compute';
}

export const sectionRoot = (s: Section): string => `/${s}`;

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
			return [{ label: 'Networking', href: '/networking' }, { label: `Segment: ${s.network}` }];
		case 'storage':
			return [{ label: 'All Storage', href: '/storage' }, { label: `Storage: ${s.storageClass}` }];
	}
}

// The inverse of hrefForScope: the Scope a path focuses (the section roots and
// non-scope routes — /vm, /catalog — read as 'all').
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

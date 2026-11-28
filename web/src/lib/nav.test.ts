import { describe, expect, it } from 'vitest';
import {
	hrefForScope,
	keepTab,
	networkingTab,
	scopeFromPath,
	sectionOf,
	storageHref,
	storageTab,
	trailForScope,
} from './nav';

describe('desktops routes', () => {
	it('puts every desktops path in its section, and only those', () => {
		expect(sectionOf('/desktops')).toBe('desktops');
		expect(sectionOf('/desktops/team-web/web-prod/pool/eng')).toBe('desktops');
		expect(sectionOf('/desktopsx')).toBeNull();
		expect(sectionOf('/compute/desktops')).toBe('compute');
		expect(sectionOf('/vm/web-prod/web-1')).toBeNull();
	});
	it('reads a pool and a desktop of the same name apart', () => {
		expect(scopeFromPath('/desktops/team-web/web-prod/pool/eng')).toEqual({
			kind: 'pool',
			project: 'team-web',
			namespace: 'web-prod',
			pool: 'eng',
		});
		expect(scopeFromPath('/desktops/team-web/web-prod/desktop/eng')).toEqual({
			kind: 'desktop',
			project: 'team-web',
			namespace: 'web-prod',
			desktop: 'eng',
		});
		expect(scopeFromPath('/desktops/team-web/web-prod')).toEqual({
			kind: 'namespace',
			project: 'team-web',
			namespace: 'web-prod',
		});
		expect(scopeFromPath('/desktops/team-web')).toEqual({ kind: 'project', project: 'team-web' });
		expect(scopeFromPath('/desktops')).toEqual({ kind: 'all' });
	});
	it('links scopes back to the paths they came from', () => {
		for (const path of [
			'/desktops/team-web',
			'/desktops/team-web/web-prod',
			'/desktops/team-web/web-prod/pool/eng',
			'/desktops/team-web/web-prod/desktop/carol-dev',
		])
			expect(hrefForScope(scopeFromPath(path), 'desktops')).toBe(path);
		expect(hrefForScope({ kind: 'project', project: 'team-web' })).toBe('/compute/team-web');
	});
	it('trails a pool under its project and namespace in Desktops', () => {
		expect(
			trailForScope(
				{ kind: 'pool', project: 'team-web', namespace: 'web-prod', pool: 'eng' },
				'Desktops',
				(p) => (p === 'team-web' ? 'Team web' : p),
				'desktops',
			),
		).toEqual([
			{ label: 'Desktops', href: '/desktops' },
			{ label: 'Team web', href: '/desktops/team-web' },
			{ label: 'web-prod', href: '/desktops/team-web/web-prod' },
			{ label: 'eng' },
		]);
	});
	it('keeps a root tab on the root only', () => {
		expect(keepTab('/desktops', 'classes')).toBe('/desktops?tab=classes');
		expect(keepTab('/desktops/team-web', 'classes')).toBe('/desktops/team-web');
		expect(keepTab('/desktops/team-web/web-prod/pool/eng', 'vms')).toBe(
			'/desktops/team-web/web-prod/pool/eng',
		);
	});
});

describe('keepTab', () => {
	it('carries the tab to an object that has it', () => {
		expect(keepTab('/compute/team-web', 'vms')).toBe('/compute/team-web?tab=vms');
		expect(keepTab('/hosts/worker-1', 'monitor')).toBe('/hosts/worker-1?tab=monitor');
		expect(keepTab('/vm/web-prod/web-2', 'console')).toBe('/vm/web-prod/web-2?tab=console');
	});
	it('drops a tab the target lacks', () => {
		expect(keepTab('/networking/web-prod/web-net', 'firewall')).toBe(
			'/networking/web-prod/web-net',
		);
		expect(keepTab('/networking/web-prod/web-net', 'vms')).toBe(
			'/networking/web-prod/web-net?tab=vms',
		);
		expect(keepTab('/networking', 'rules')).toBe('/networking');
		expect(keepTab('/hosts/worker-1', 'permissions')).toBe('/hosts/worker-1');
		expect(keepTab('/compute/team-web', 'snapshots')).toBe('/compute/team-web');
	});
	it('leaves summary, non-inventory routes and explicit queries alone', () => {
		expect(keepTab('/compute', 'summary')).toBe('/compute');
		expect(keepTab('/compute', null)).toBe('/compute');
		expect(keepTab('/storage?tab=images', 'vms')).toBe('/storage?tab=images');
		expect(keepTab('/changes', 'vms')).toBe('/changes');
	});
	it('keeps a Changes tab across scopes', () => {
		expect(keepTab('/changes/team-web', 'history')).toBe('/changes/team-web?tab=history');
		expect(keepTab('/changes', 'review')).toBe('/changes?tab=review');
		expect(keepTab('/changes/team-web', 'changes')).toBe('/changes/team-web');
	});
});

describe('networkingTab', () => {
	it('maps the old root tabs and defaults to the map', () => {
		expect(networkingTab('security')).toBe('firewall');
		expect(networkingTab('summary')).toBe('map');
		expect(networkingTab(null)).toBe('map');
		expect(networkingTab('uplinks')).toBe('uplinks');
	});
});

describe('storage routes', () => {
	it('keeps a storage tab on the root, not on a class drill-in', () => {
		expect(keepTab('/storage', 'snapshots')).toBe('/storage?tab=snapshots');
		expect(keepTab('/storage/bulk-hdd', 'snapshots')).toBe('/storage/bulk-hdd');
		expect(keepTab('/storage/bulk-hdd', 'vms')).toBe('/storage/bulk-hdd');
	});
	it('builds root views with an explicit tab and only set parameters', () => {
		expect(storageHref('disks', { f: 'nolm', project: 'team-web', host: undefined })).toBe(
			'/storage?tab=disks&f=nolm&project=team-web',
		);
		expect(keepTab(storageHref('library'), 'disks')).toBe('/storage?tab=library');
		expect(storageTab('images')).toBe('images');
		expect(storageTab('summary')).toBe('library');
	});
	it('trails a host under Hosts', () => {
		expect(trailForScope({ kind: 'node', node: 'host-07' })).toEqual([
			{ label: 'Hosts', href: '/hosts' },
			{ label: 'host-07' },
		]);
	});
	it('names a project by its title and links it by its name', () => {
		const title = (p: string) => (p === 'u-acme-web' ? 'acme/web' : p);
		expect(
			trailForScope(
				{ kind: 'namespace', project: 'u-acme-web', namespace: 'u-acme-web-prod' },
				'All VMs',
				title,
			),
		).toEqual([
			{ label: 'All VMs', href: '/compute' },
			{ label: 'acme/web', href: '/compute/u-acme-web' },
			{ label: 'u-acme-web-prod' },
		]);
	});
	it('trails a class under Storage and Classes', () => {
		expect(trailForScope({ kind: 'storage', storageClass: 'bulk-hdd' })).toEqual([
			{ label: 'Storage', href: '/storage?tab=library' },
			{ label: 'Classes', href: '/storage?tab=classes' },
			{ label: 'bulk-hdd' },
		]);
	});
});

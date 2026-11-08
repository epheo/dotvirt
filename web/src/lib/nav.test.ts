import { describe, expect, it } from 'vitest';
import { keepTab, networkingTab, storageHref, storageTab, trailForScope } from './nav';

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
		expect(keepTab('/catalog?kind=images', 'vms')).toBe('/catalog?kind=images');
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
	it('trails a class under Storage and Classes', () => {
		expect(trailForScope({ kind: 'storage', storageClass: 'bulk-hdd' })).toEqual([
			{ label: 'Storage', href: '/storage?tab=library' },
			{ label: 'Classes', href: '/storage?tab=classes' },
			{ label: 'bulk-hdd' },
		]);
	});
});

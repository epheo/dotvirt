import { describe, expect, it } from 'vitest';
import { keepTab } from './nav';

describe('keepTab', () => {
	it('carries the tab to an object that has it', () => {
		expect(keepTab('/compute/team-web', 'vms')).toBe('/compute/team-web?tab=vms');
		expect(keepTab('/hosts/worker-1', 'monitor')).toBe('/hosts/worker-1?tab=monitor');
		expect(keepTab('/vm/web-prod/web-2', 'console')).toBe('/vm/web-prod/web-2?tab=console');
	});
	it('drops a tab the target lacks', () => {
		expect(keepTab('/networking/web-net', 'security')).toBe('/networking/web-net');
		expect(keepTab('/hosts/worker-1', 'permissions')).toBe('/hosts/worker-1');
		expect(keepTab('/compute/team-web', 'snapshots')).toBe('/compute/team-web');
	});
	it('leaves summary, non-inventory routes and explicit queries alone', () => {
		expect(keepTab('/compute', 'summary')).toBe('/compute');
		expect(keepTab('/compute', null)).toBe('/compute');
		expect(keepTab('/catalog?kind=images', 'vms')).toBe('/catalog?kind=images');
		expect(keepTab('/changes', 'vms')).toBe('/changes');
	});
});

import { describe, expect, it } from 'vitest';
import type { VM } from '#lib/api.js';
import { hostState, hostsAttention, vmHeadline, vmStats } from './headline';

describe('hostState', () => {
	it('ranks maintenance over not ready over a plain cordon', () => {
		expect(hostState({ ready: true })).toEqual({ tone: 'ok', label: 'Ready' });
		expect(hostState({ ready: true, unschedulable: true })).toEqual({
			tone: 'warn',
			label: 'Cordoned',
		});
		expect(hostState({ ready: false, unschedulable: true }).label).toBe('Not ready');
		expect(hostState({ ready: false, unschedulable: true, maintenance: true }).label).toBe(
			'Maintenance',
		);
	});
});

describe('hostsAttention', () => {
	it('says nothing while every host is ready', () => {
		expect(hostsAttention([{ ready: true }, { ready: true }])).toBeNull();
		expect(hostsAttention([])).toBeNull();
	});
	it('names the hosts that need a person, worst first', () => {
		expect(hostsAttention([{ ready: true }, { ready: true, maintenance: true }])).toEqual({
			tone: 'warn',
			text: '1 host in maintenance',
		});
		expect(
			hostsAttention([
				{ ready: true, unschedulable: true },
				{ ready: false },
				{ ready: false },
				{ ready: true, maintenance: true, unschedulable: true },
			]),
		).toEqual({ tone: 'danger', text: '2 hosts not ready, 1 in maintenance, 1 cordoned' });
	});
});

describe('vmStats', () => {
	const vm = (name: string, over: Partial<VM> = {}): VM => ({
		namespace: 'ns',
		name,
		power: 'On',
		sourceFile: '',
		sync: 'Synced',
		phase: 'Running',
		...over,
	});
	const vms = [
		vm('a'),
		vm('b', { phase: undefined, power: 'Off' }),
		vm('c', { paused: true }),
		vm('d', { phase: 'Starting' }),
		vm('e'),
	];

	it('counts issues, changes and phases, running first', () => {
		const s = vmStats(vms, new Set(['ns/a']), (k) => k === 'ns/e' || k === 'ns/b');
		expect(s.issues).toBe(1);
		expect(s.inChange).toBe(2);
		expect(s.phases).toEqual([
			['Running', 2],
			['Paused', 1],
			['Stopped', 1],
			['Starting', 1],
		]);
	});

	it('reads as one status line, issues first', () => {
		expect(vmHeadline(vmStats(vms, new Set(['ns/a']), () => false))).toEqual({
			tone: 'danger',
			text: '1 with issues, 2 running, 1 paused, 1 stopped, 1 starting',
		});
		expect(vmHeadline(vmStats([vm('a')], new Set(), () => false))).toEqual({
			tone: 'ok',
			text: '1 running',
		});
		expect(
			vmHeadline(vmStats([vm('b', { phase: undefined, power: 'Off' })], new Set(), () => false)),
		).toEqual({ tone: 'off', text: '1 stopped' });
		expect(vmHeadline(vmStats([], new Set(), () => false))).toBeNull();
	});
});

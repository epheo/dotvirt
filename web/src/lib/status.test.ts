import { describe, expect, it } from 'vitest';
import type { VM } from '#lib/api.js';
import {
	TONE_DOT,
	TONE_PILL,
	TONE_TEXT,
	severityTone,
	taskTone,
	vmState,
	vmStateRank,
	type Tone,
} from '#lib/status.js';

const TONES: Tone[] = ['ok', 'warn', 'danger', 'info', 'neutral'];

describe('tone class maps', () => {
	it.each([
		['TONE_DOT', TONE_DOT],
		['TONE_PILL', TONE_PILL],
		['TONE_TEXT', TONE_TEXT],
	])('%s covers every tone with a non-empty class', (_name, map) => {
		for (const tone of TONES) expect(map[tone]).toBeTruthy();
		expect(Object.keys(map).sort()).toEqual([...TONES].sort());
	});
});

describe('severityTone', () => {
	it('maps alert severities onto tones', () => {
		expect(severityTone('critical')).toBe('danger');
		expect(severityTone('warning')).toBe('warn');
		expect(severityTone('info')).toBe('neutral');
		expect(severityTone(undefined)).toBe('neutral');
	});
});

describe('taskTone', () => {
	it('maps task-dock rows onto tones', () => {
		expect(taskTone({ kind: 'drift' })).toBe('warn');
		expect(taskTone({ kind: 'migration', active: true })).toBe('info');
		expect(taskTone({ kind: 'migration', ok: true })).toBe('ok');
		expect(taskTone({ kind: 'migration', ok: false })).toBe('danger');
		expect(taskTone({ kind: 'sync', active: true })).toBe('info');
		expect(taskTone({ kind: 'sync' })).toBe('ok');
		expect(taskTone({ kind: 'action', ok: true })).toBe('ok');
		expect(taskTone({ kind: 'action', ok: false })).toBe('danger');
		expect(taskTone({ kind: 'pr' })).toBe('ok');
		expect(taskTone({ kind: 'staged' })).toBe('info');
	});
});

describe('vmState', () => {
	const vm = (over: Partial<VM>): VM => ({
		namespace: 'ns',
		name: 'vm',
		power: 'On',
		sourceFile: '',
		sync: 'Synced',
		phase: 'Running',
		...over,
	});

	it('says only the run state when nothing needs a person', () => {
		expect(vmState(vm({ health: 'Healthy' }))).toEqual({
			tone: 'ok',
			word: 'Running',
			exception: undefined,
		});
		expect(vmState(vm({ power: 'Off', phase: undefined })).word).toBe('Stopped');
		expect(vmState(vm({ paused: true })).word).toBe('Paused');
	});

	it('names the worst exception, with the cluster words as detail', () => {
		const v = vmState(vm({ syncError: 'denied', health: 'Degraded', sync: 'OutOfSync' }));
		expect(v.exception).toEqual({ tone: 'danger', label: 'Apply refused', detail: 'denied' });
		expect(vmState(vm({ sync: 'OutOfSync' })).exception?.label).toBe('Differs from git');
		expect(vmState(vm({ sync: 'NotTracked' })).exception?.label).toBe('Not in git');
		expect(vmState(vm({ sync: 'Pending' })).exception?.label).toBe('Pending sync');
	});

	it('ranks a crash loop with a refused apply, above drift and suggestions', () => {
		const rank = (over: Partial<VM>) => vmStateRank(vmState(vm(over)));
		expect(rank({ phase: 'CrashLoopBackOff' })).toBe(rank({ syncError: 'x' }));
		expect(rank({ syncError: 'x' })).toBeGreaterThan(rank({ sync: 'OutOfSync' }));
		expect(rank({ sync: 'OutOfSync' })).toBeGreaterThan(rank({ sync: 'NotTracked' }));
		expect(rank({ sync: 'NotTracked' })).toBeGreaterThan(rank({ phase: 'Migrating' }));
	});
});

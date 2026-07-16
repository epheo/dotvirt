import { describe, expect, it } from 'vitest';
import { TONE_DOT, TONE_PILL, TONE_TEXT, severityTone, taskTone, type Tone } from '$lib/status';

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

import { describe, expect, it } from 'vitest';
import { metricsGap } from './metrics.js';

describe('metricsGap', () => {
	it('lets whole and partial reads show their numbers', () => {
		expect(metricsGap(undefined)).toBe('');
		expect(metricsGap('')).toBe('');
		expect(metricsGap('partial')).toBe('');
	});

	it('says why the other reads hold nothing', () => {
		for (const r of ['forbidden', 'unreachable', 'unconfigured'])
			expect(metricsGap(r)).toMatch(/^Metrics are not available: /);
		expect(metricsGap('scope')).toMatch(/Pick a project/);
		expect(metricsGap('a-new-reason')).toBe('Metrics are not available.');
	});
});

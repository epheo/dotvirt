import { describe, expect, it } from 'vitest';
import { retryDelay } from '$lib/api';

describe('retryDelay', () => {
	it('starts at 0.5s and doubles per attempt', () => {
		expect([1, 2, 3, 4, 5].map(retryDelay)).toEqual([500, 1000, 2000, 4000, 8000]);
	});

	it('caps at 16s', () => {
		expect(retryDelay(6)).toBe(16000);
		expect(retryDelay(7)).toBe(16000);
		expect(retryDelay(100)).toBe(16000);
	});

	it('clamps below the first attempt to 0.5s', () => {
		expect(retryDelay(0)).toBe(500);
		expect(retryDelay(-3)).toBe(500);
	});
});

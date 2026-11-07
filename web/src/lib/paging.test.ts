import { describe, expect, it } from 'vitest';
import { pageFor, pageOf, pageSlice, rangeLabel } from './paging';

describe('pageSlice', () => {
	it('slices a page and says where it sits', () => {
		const s = pageSlice(389, 1, 50);
		expect(s).toEqual({ page: 1, pages: 8, start: 0, end: 50, total: 389 });
		expect(rangeLabel(s)).toBe('1-50');
	});
	it('ends the last page at the last row', () => {
		const s = pageSlice(389, 8, 50);
		expect([s.start, s.end]).toEqual([350, 389]);
		expect(rangeLabel(s)).toBe('351-389');
	});
	it('clamps a page past the end, as when a live update shrinks the list', () => {
		expect(pageSlice(120, 9, 50).page).toBe(3);
		expect(pageSlice(120, 0, 50).page).toBe(1);
		expect(pageSlice(120, Number.NaN, 50).page).toBe(1);
	});
	it('reads an empty list as one empty page', () => {
		const s = pageSlice(0, 4, 50);
		expect(s).toEqual({ page: 1, pages: 1, start: 0, end: 0, total: 0 });
		expect(rangeLabel(s)).toBe('0');
	});
	it('groups thousands', () => {
		expect(rangeLabel(pageSlice(2846, 57, 50))).toBe('2,801-2,846');
	});
});

describe('pageOf / pageFor', () => {
	it('finds the page holding a row', () => {
		expect(pageOf(0, 50)).toBe(1);
		expect(pageOf(49, 50)).toBe(1);
		expect(pageOf(50, 50)).toBe(2);
		expect(pageOf(-1, 50)).toBe(1);
	});
	it('keeps a picked row reachable, else starts over', () => {
		const keys = Array.from({ length: 120 }, (_, i) => `ns/vm-${i}`);
		expect(pageFor(keys, 'ns/vm-75', 25)).toBe(4);
		expect(pageFor(keys, 'ns/gone', 25)).toBe(1);
		expect(pageFor(keys, null, 25)).toBe(1);
	});
});

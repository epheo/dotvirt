import { describe, expect, it } from 'vitest';
import { bySize, fold, leafWindow, matches } from './rail';

type G = { name: string; n: number };
const g = (name: string, n: number): G => ({ name, n });

describe('bySize', () => {
	it('ranks the biggest first and reads ties in name order', () => {
		const ranked = bySize(
			[g('tenant-10', 5), g('team-web', 412), g('tenant-2', 5)],
			(x) => x.n,
			(x) => x.name,
		);
		expect(ranked.map((x) => x.name)).toEqual(['team-web', 'tenant-2', 'tenant-10']);
	});
});

describe('fold', () => {
	const ranked = Array.from({ length: 52 }, (_, i) => g(`p-${i}`, 100 - i));
	it('shows the top groups and folds the rest', () => {
		const { shown, folded } = fold(ranked, 5);
		expect(shown).toHaveLength(5);
		expect(folded).toHaveLength(47);
	});
	it('always shows the group in view', () => {
		const { shown, folded } = fold(ranked, 5, (x) => x.name === 'p-40');
		expect(shown.map((x) => x.name)).toContain('p-40');
		expect(folded).toHaveLength(46);
	});
	it('never folds a single group', () => {
		expect(fold(ranked.slice(0, 6), 5).folded).toEqual([]);
		expect(fold(ranked.slice(0, 7), 5).folded).toHaveLength(2);
	});
});

describe('leafWindow', () => {
	const vms = Array.from({ length: 412 }, (_, i) => `web-${i + 1}`);
	it('lists the first ten and counts the rest', () => {
		const w = leafWindow(vms, 10);
		expect(w.shown).toHaveLength(10);
		expect(w.more).toBe(402);
	});
	it('adds the VM in view when it sits further down', () => {
		const w = leafWindow(vms, 10, (v) => v === 'web-300');
		expect(w.shown.at(-1)).toBe('web-300');
		expect(w.more).toBe(401);
	});
	it('lists a small container whole', () => {
		expect(leafWindow(vms.slice(0, 4), 10)).toEqual({ shown: vms.slice(0, 4), more: 0 });
	});
});

describe('matches', () => {
	it('is a case-blind substring over any name; empty matches all', () => {
		expect(matches('WEB', 'team-web')).toBe(true);
		expect(matches('prod', 'team-db', 'db-prod')).toBe(true);
		expect(matches('zzz', 'team-db')).toBe(false);
		expect(matches('  ', 'team-db')).toBe(true);
	});
});

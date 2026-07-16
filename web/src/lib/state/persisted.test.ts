import { beforeEach, describe, expect, it } from 'vitest';
import { persisted } from '$lib/state/persisted.svelte';

// Hand-rolled localStorage: enough surface for persisted(), no jsdom.
const store = new Map<string, string>();
let failWrites = false;

beforeEach(() => {
	store.clear();
	failWrites = false;
	globalThis.localStorage = {
		get length() {
			return store.size;
		},
		clear: () => store.clear(),
		getItem: (k: string) => store.get(k) ?? null,
		key: (i: number) => [...store.keys()][i] ?? null,
		removeItem: (k: string) => void store.delete(k),
		setItem: (k: string, v: string) => {
			if (failWrites) throw new Error('quota exceeded');
			store.set(k, v);
		},
	} as Storage;
});

describe('persisted', () => {
	it('returns the initial value when storage is empty', () => {
		expect(persisted('dotvirt.test', 'fallback').value).toBe('fallback');
	});

	it('prefers a stored value over the initial', () => {
		store.set('dotvirt.test', JSON.stringify({ open: true }));
		expect(persisted('dotvirt.test', { open: false }).value).toEqual({ open: true });
	});

	it('writes whole-value assignments through to storage', () => {
		const p = persisted('dotvirt.test', { open: false });
		p.value = { open: true };
		expect(p.value).toEqual({ open: true });
		expect(store.get('dotvirt.test')).toBe(JSON.stringify({ open: true }));
	});

	it('falls back to the initial value on a corrupt entry', () => {
		store.set('dotvirt.test', '{not json');
		expect(persisted('dotvirt.test', 42).value).toBe(42);
	});

	it('keeps the in-memory value when storage writes fail', () => {
		const p = persisted('dotvirt.test', 'a');
		failWrites = true;
		p.value = 'b';
		expect(p.value).toBe('b');
		expect(store.has('dotvirt.test')).toBe(false);
	});
});

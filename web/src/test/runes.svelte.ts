import { flushSync } from 'svelte';

// Rune plumbing for unit tests: code under test ($lib/resource.svelte.ts) may
// register $effects, which need a root scope outside components. Runes only
// compile in .svelte.ts modules, so the wrappers live here and the test files
// stay rune-free.

// effectRoot runs fn inside $effect.root, flushes the initial effects, and
// returns the teardown.
export function effectRoot(fn: () => void): () => void {
	const stop = $effect.root(fn);
	flushSync();
	return stop;
}

// box is a reactive cell tests can flip to drive keyed effects.
export function box<T>(initial: T): { value: T } {
	let value = $state(initial);
	return {
		get value() {
			return value;
		},
		set value(v: T) {
			value = v;
		},
	};
}

export { flushSync };

import { createSubscriber } from 'svelte/reactivity';

// An elapsed time ("up 5m", "2m ago") reads now(), so it advances on its own;
// reading Date.now() alone would only update when its object changes. The timer
// runs only while something on screen reads it.
const tick = createSubscriber((update) => {
	const id = setInterval(update, 1000);
	return () => clearInterval(id);
});

export function now(): number {
	tick();
	return Date.now();
}

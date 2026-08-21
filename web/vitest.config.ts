import { compileModule } from 'svelte/compiler';
import { fileURLToPath } from 'node:url';
import { defineConfig, type Plugin } from 'vitest/config';

// Unit tests cover pure logic plus the rune modules (persisted, resource,
// action): node environment, no jsdom. $app/environment is stubbed to
// browser=true so persisted() runs its storage path against the test's
// hand-rolled localStorage.
//
// Rune modules must compile for the CLIENT: vitest transforms through Vite's
// SSR pipeline, and vite-plugin-svelte hard-couples generate to that SSR flag —
// server-compiled `$effect.root(fn)` is `() => {}`, so resource()'s keyed
// effects would silently never run. This post-plugin (after esbuild has
// stripped the types) compiles *.svelte.ts for the client instead; the client
// runtime needs no DOM for state/effects.
function svelteRuneModules(): Plugin {
	return {
		name: 'svelte-rune-modules-client',
		enforce: 'post',
		transform(code, id) {
			if (!/\.svelte\.ts$/.test(id)) return null;
			return compileModule(code, { generate: 'client', filename: id }).js;
		},
	};
}

export default defineConfig({
	plugins: [svelteRuneModules()],
	resolve: {
		// The client svelte runtime, matching the client compile above.
		conditions: ['browser'],
		alias: {
			$lib: fileURLToPath(new URL('./src/lib', import.meta.url)),
			'$app/environment': fileURLToPath(new URL('./src/test/app-environment.ts', import.meta.url)),
		},
	},
	ssr: {
		resolve: {
			conditions: ['browser'],
		},
	},
	test: {
		include: ['src/**/*.test.ts'],
	},
});

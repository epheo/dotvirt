import { svelte } from '@sveltejs/vite-plugin-svelte';
import { svelteTesting } from '@testing-library/svelte/vite';
import { compileModule } from 'svelte/compiler';
import { fileURLToPath } from 'node:url';
import { defineConfig, type Plugin } from 'vitest/config';

// Two projects. unit: pure logic plus the rune modules (persisted, resource,
// action): node environment, no jsdom. $app/env is stubbed to
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

// component (*.component.test.ts): mounted .svelte components in jsdom via
// @testing-library/svelte. jsdom runs Vite's client pipeline, so the stock
// svelte plugin compiles components and rune modules for the client itself.
const alias = (path: string) => fileURLToPath(new URL(path, import.meta.url));

export default defineConfig({
	resolve: {
		// The client svelte runtime, matching the client compile above.
		conditions: ['browser'],
		alias: {
			'$app/env': alias('./src/test/app-env.ts'),
			'$app/navigation': alias('./src/test/app-navigation.ts'),
		},
	},
	ssr: {
		resolve: {
			conditions: ['browser'],
		},
	},
	test: {
		projects: [
			{
				extends: true,
				plugins: [svelteRuneModules()],
				test: {
					name: 'unit',
					include: ['src/**/*.test.ts'],
					exclude: ['src/**/*.component.test.ts'],
				},
			},
			{
				extends: true,
				plugins: [
					// The build's runes rule (vite.config.ts): app components are runes-only.
					svelte({
						compilerOptions: {
							runes: ({ filename }) =>
								filename.split(/[/\\]/).includes('node_modules') ? undefined : true,
						},
					}),
					svelteTesting(),
				],
				test: {
					name: 'component',
					include: ['src/**/*.component.test.ts'],
					environment: 'jsdom',
				},
			},
		],
	},
});

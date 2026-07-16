import { svelte } from '@sveltejs/vite-plugin-svelte';
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vitest/config';

// Unit tests cover pure logic only: node environment, no jsdom, no SvelteKit
// plugin. The svelte plugin is here solely so persisted.svelte.ts's $state
// compiles; $app/environment is stubbed to browser=true so persisted() runs
// its storage path against the test's hand-rolled localStorage.
export default defineConfig({
	plugins: [svelte()],
	resolve: {
		alias: {
			$lib: fileURLToPath(new URL('./src/lib', import.meta.url)),
			'$app/environment': fileURLToPath(new URL('./src/test/app-environment.ts', import.meta.url)),
		},
	},
	test: {
		include: ['src/**/*.test.ts'],
	},
});

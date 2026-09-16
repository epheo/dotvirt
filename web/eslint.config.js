import js from '@eslint/js';
import svelte from 'eslint-plugin-svelte';
import globals from 'globals';
import ts from 'typescript-eslint';
import svelteConfig from './svelte.config.js';

// Lint is what svelte-check cannot flag: unused bindings and the shape rules
// of the recommended sets. Style is prettier's; the types are svelte-check's.
export default ts.config(
	js.configs.recommended,
	...ts.configs.recommended,
	...svelte.configs.recommended,
	{
		languageOptions: { globals: { ...globals.browser, ...globals.node } },
		rules: {
			'@typescript-eslint/no-unused-vars': [
				'error',
				{ argsIgnorePattern: '^_', varsIgnorePattern: '^_', caughtErrors: 'none' },
			],
			// A best-effort step's empty catch is the idiom, not an omission.
			'no-empty': ['error', { allowEmptyCatch: true }],
			// The SPA has no base path: its hrefs and goto targets are the URL
			// scheme nav.ts owns, never resolve()d.
			'svelte/no-navigation-without-resolve': 'off',
			// Flags the local Map/Set accumulators inside $derived as well; a
			// SvelteMap/SvelteSet pass for the few reactive ones is separate work.
			'svelte/prefer-svelte-reactivity': 'off',
			// Misses the state_referenced_locally warnings the compiler raises on a
			// multi-line prop read; svelte-check is the authority on ignores.
			'svelte/no-unused-svelte-ignore': 'off',
		},
	},
	{
		files: ['**/*.svelte', '**/*.svelte.ts', '**/*.svelte.js'],
		languageOptions: { parserOptions: { parser: ts.parser, svelteConfig } },
		// A bare read inside $effect is how a dependency is registered.
		rules: { '@typescript-eslint/no-unused-expressions': 'off' },
	},
	{ ignores: ['build/', '.svelte-kit/', 'src/lib/model.gen.ts', 'e2e/fixture/'] },
);

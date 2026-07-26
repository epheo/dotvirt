import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

// Kit options (adapter, SPA fallback) live in vite.config.ts, the copy the
// build actually reads; only the preprocessor stays here for svelte-check
// and prettier, which load this file directly.
/** @type {import('@sveltejs/kit').Config} */
export default {
	preprocess: vitePreprocess()
};

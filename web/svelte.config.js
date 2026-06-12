import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
export default {
	preprocess: vitePreprocess(),
	kit: {
		// SPA: the +layout sets ssr=false / prerender=true, so adapter-static emits a
		// single index.html (in build/) that the Go binary serves for every route.
		adapter: adapter({ fallback: 'index.html', strict: false })
	}
};

import { redirect } from '@sveltejs/kit';
import { sectionRoot } from '#lib/nav.js';
import { lastSection } from '#lib/state/nav.svelte.js';

// The app's home is the last-visited inventory section (Compute on first run).
export function load() {
	redirect(307, sectionRoot(lastSection.value));
}

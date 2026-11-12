import { redirect } from '@sveltejs/kit';
import { sectionRoot } from '#lib/nav.js';
import { lastSection } from '#lib/state/nav.svelte.js';

// The app's home is the last-visited inventory section (Compute on first run).
// The query rides along: a failed sign-in lands on / with sso_error, which the
// login screen shows wherever it renders.
export function load({ url }) {
	redirect(307, sectionRoot(lastSection.value) + url.search);
}

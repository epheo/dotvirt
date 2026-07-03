import { redirect } from '@sveltejs/kit';

// The app's home is the Compute inventory.
export function load() {
	redirect(307, '/compute');
}

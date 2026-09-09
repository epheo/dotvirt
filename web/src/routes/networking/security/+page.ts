import { redirect } from '@sveltejs/kit';

// Security is a tab on the Networking root; the old path stays a deep link.
export function load({ url }) {
	const to = new URL('/networking', url);
	to.searchParams.set('tab', 'security');
	const tenant = url.searchParams.get('tenant');
	if (tenant) to.searchParams.set('tenant', tenant);
	redirect(307, to.pathname + to.search);
}

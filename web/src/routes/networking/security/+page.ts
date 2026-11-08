import { redirect } from '@sveltejs/kit';

// The policy plane is the Networking root's Firewall tab; the old path stays a deep link.
export function load({ url }) {
	const to = new URL('/networking', url);
	to.searchParams.set('tab', 'firewall');
	const tenant = url.searchParams.get('tenant');
	if (tenant) to.searchParams.set('tenant', tenant);
	redirect(307, to.pathname + to.search);
}

import { redirect } from '@sveltejs/kit';
import { templatesHref } from '#lib/nav.js';
import type { PageLoad } from './$types';

// Links to /catalog open Compute's Templates tab, a template link its template.
export const load: PageLoad = ({ url }) => {
	const kind = url.searchParams.get('kind');
	const item = !kind || kind === 'templates' ? url.searchParams.get('item') : null;
	redirect(307, templatesHref(item ?? undefined));
};

import type { AuthMethods } from './api';

// What the sign-in screen does, from the backend's sign-in paths and the URL.
// redirect: go straight to the provider; button: one provider button and why
// the screen did not redirect; token: the token form (no provider is ready, or
// ?token asked for it).
export type SignInView =
	| { kind: 'redirect'; name: string; to: string }
	| { kind: 'button'; name: string; to: string; reason: 'failed' | 'signed-out' | 'repeat' }
	| { kind: 'token' };

// The URL markers the screen reads. sso_error: the callback failed. signed_out:
// a sign-out just ended the session, so redirecting would sign the user straight
// back in through the provider's live session. token: an admin's break-glass.
const MARKERS = ['sso_error', 'signed_out', 'token'];

// A redirect within this window of the last one in this tab shows the button
// instead: a session the browser does not keep (or one the API refuses at once)
// would otherwise bounce between the provider and this screen forever.
export const REPEAT_WINDOW_MS = 60_000;

// The provider a ready sign-in flow starts at: OIDC before OpenShift, as the
// backend never offers both. None while OpenShift sign-in is still pending.
export function provider(m: AuthMethods): { name: string; start: string } | null {
	if (m.oidc) return { name: m.oidc, start: '/api/auth/oidc' };
	if (m.sso && !m.ssoPending) return { name: 'OpenShift', start: '/api/auth/openshift' };
	return null;
}

// The page to come back to after signing in: this URL minus the screen's own
// markers, or '' for the root. The backend re-checks it (SafeReturn).
export function returnPath(url: PageURL): string {
	const q = new URLSearchParams(url.search);
	for (const m of MARKERS) q.delete(m);
	const search = q.toString();
	const path = url.pathname + (search ? `?${search}` : '') + url.hash;
	return path === '/' ? '' : path;
}

// The parts of the page URL the screen reads; SvelteKit's page.url is
// read-only, so not a URL.
type PageURL = Pick<URL, 'pathname' | 'search' | 'hash'> & {
	searchParams: { has(name: string): boolean };
};

// methods is null while they load (or when they failed: the token form, as the
// only path known to work). recent: a redirect ran within REPEAT_WINDOW_MS.
export function signInView(methods: AuthMethods | null, url: PageURL, recent: boolean): SignInView {
	const q = url.searchParams;
	const p = methods && provider(methods);
	if (!p || q.has('token')) return { kind: 'token' };
	const ret = returnPath(url);
	const to = ret ? `${p.start}?return=${encodeURIComponent(ret)}` : p.start;
	if (q.has('sso_error')) return { kind: 'button', name: p.name, to, reason: 'failed' };
	if (q.has('signed_out')) return { kind: 'button', name: p.name, to, reason: 'signed-out' };
	if (recent) return { kind: 'button', name: p.name, to, reason: 'repeat' };
	return { kind: 'redirect', name: p.name, to };
}

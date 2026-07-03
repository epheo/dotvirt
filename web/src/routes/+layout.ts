// SPA mode: no server-side rendering, no prerendered pages — adapter-static
// emits only the fallback index.html, which the Go binary serves for every
// route (internal/api spaRouter), so deep links and refresh resolve client-side.
export const ssr = false;
export const prerender = false;
export const trailingSlash = 'ignore';

// SPA mode: no server-side rendering, prerender the shell so adapter-static can
// emit a single index.html that the Go binary serves for every route.
export const ssr = false;
export const prerender = true;
export const trailingSlash = 'ignore';

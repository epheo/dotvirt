package api

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// withSecurityHeaders hardens every same-origin response. Applied only in
// production (StaticDir set): in dev the UI lives on Vite's origin and these
// headers on bare JSON would gate nothing.
func withSecurityHeaders(csp string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if csp != "" {
			h.Set("Content-Security-Policy", csp)
		}
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// Bare inline <script> blocks only: SvelteKit's bootstrap and the theme stamper.
// External scripts (src=) are covered by 'self'.
var inlineScriptRe = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

// buildCSP derives the policy from the index.html actually served: the SvelteKit
// bootstrap script is inlined with per-build asset paths, so its hash cannot be a
// constant here — it is computed at startup instead. style 'unsafe-inline' is for
// Svelte's style: attribute directives; connect-src gains the cdi-uploadproxy
// origin because image uploads stream from the browser straight to it.
func buildCSP(indexHTML []byte, uploadProxyURL string) string {
	scriptSrc := []string{"'self'"}
	for _, m := range inlineScriptRe.FindAllSubmatch(indexHTML, -1) {
		sum := sha256.Sum256(m[1])
		scriptSrc = append(scriptSrc, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	connectSrc := []string{"'self'"}
	if o := originOf(uploadProxyURL); o != "" {
		connectSrc = append(connectSrc, o)
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src " + strings.Join(scriptSrc, " "),
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"connect-src " + strings.Join(connectSrc, " "),
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}

// originOf reduces a URL to its scheme://host origin for a CSP source, "" when
// unparsable so a malformed config never widens the policy.
func originOf(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

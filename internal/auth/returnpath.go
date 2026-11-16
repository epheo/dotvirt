package auth

import (
	"net/url"
	"path"
	"strings"
)

// maxReturnPath bounds the path a sign-in returns to; it rides the sealed
// state cookie, which must stay well under a browser's cookie size limit.
const maxReturnPath = 1024

// SafeReturn is p when it is a same-origin page path a sign-in may land on,
// else "/". A relative path only: no scheme or host, and none of the prefixes
// a browser reads as another origin ("//", or a backslash it treats as a
// slash). Never an API path: landing on /api/auth/... would start the sign-in
// again and loop. That check runs on the path as the router sees it, decoded
// (%61pi is api). A return that cleaning would change is refused outright:
// http.Redirect path-cleans everything before the '?', fragment included, so
// /./api and /x#/../api both land on /api.
func SafeReturn(p string) string {
	if p == "" || len(p) > maxReturnPath || p[0] != '/' || strings.HasPrefix(p, "//") || unsafeChars(p) {
		return "/"
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return "/"
	}
	d := u.Path
	if changedByCleaning(d) || unsafeChars(d) {
		return "/"
	}
	if raw, _, _ := strings.Cut(p, "?"); changedByCleaning(raw) {
		return "/"
	}
	if d == "/api" || strings.HasPrefix(d, "/api/") {
		return "/"
	}
	return p
}

// changedByCleaning: path.Clean would rewrite s, a trailing slash aside (which
// http.Redirect keeps).
func changedByCleaning(s string) bool {
	clean := path.Clean(s)
	return clean != s && clean != strings.TrimSuffix(s, "/")
}

// unsafeChars: a control character, or a backslash a browser reads as a slash.
func unsafeChars(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '\\' })
}

// withReturn appends the return path to a sealed state value. A newline never
// occurs in either part: the state fields are base64url and SafeReturn
// refuses control characters.
func withReturn(state, ret string) string {
	return state + "\n" + SafeReturn(ret)
}

// splitReturn undoes withReturn. A state sealed without one returns "/".
func splitReturn(sealed string) (state, ret string) {
	state, ret, ok := strings.Cut(sealed, "\n")
	if !ok {
		return sealed, "/"
	}
	return state, SafeReturn(ret)
}

package auth

import (
	"net/url"
	"strings"
)

// maxReturnPath bounds the path a sign-in returns to; it rides the sealed
// state cookie, which must stay well under a browser's cookie size limit.
const maxReturnPath = 1024

// SafeReturn is p when it is a same-origin page path a sign-in may land on,
// else "/". A relative path only: no scheme or host, and none of the prefixes
// a browser reads as another origin ("//", or a backslash it treats as a
// slash). Never an API path: landing on /api/auth/... would start the sign-in
// again and loop.
func SafeReturn(p string) string {
	if p == "" || len(p) > maxReturnPath || p[0] != '/' || strings.ContainsAny(p, "\\\x00\r\n\t") {
		return "/"
	}
	if strings.HasPrefix(p, "//") || p == "/api" || strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/api?") {
		return "/"
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return "/"
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return "/"
		}
	}
	return p
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

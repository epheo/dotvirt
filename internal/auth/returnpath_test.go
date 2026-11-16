package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
)

func TestSafeReturnKeepsOnlySameOriginPages(t *testing.T) {
	for in, want := range map[string]string{
		"/compute/vms/ns/web":   "/compute/vms/ns/web",
		"/changes?pr=3#top":     "/changes?pr=3#top",
		"/":                     "/",
		"":                      "/",
		"compute":               "/",
		"//evil.example/x":      "/",
		"/\\evil.example":       "/",
		"/a\\b":                 "/",
		"https://evil.example/": "/",
		"javascript:alert(1)":   "/",
		"/x\r\nSet-Cookie: a=b": "/",
		"/x\ty":                 "/",
		"/x\x7fy":               "/",
		"/api":                  "/",
		"/api/auth/oidc":        "/",
		"/api?x=1":              "/",
		"/apiary":               "/apiary",
		"/" + string(make([]byte, maxReturnPath)): "/",
	} {
		if got := SafeReturn(in); got != want {
			t.Errorf("SafeReturn(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStateCarriesTheReturnPath(t *testing.T) {
	for in, want := range map[string]string{"/compute": "/compute", "//evil": "/", "": "/"} {
		state, ret := splitReturn(withReturn("s.v.n", in))
		if state != "s.v.n" || ret != want {
			t.Errorf("round trip of %q = (%q, %q), want (s.v.n, %q)", in, state, ret, want)
		}
	}
	if state, ret := splitReturn("s.v.n"); state != "s.v.n" || ret != "/" {
		t.Errorf("a state sealed without a return = (%q, %q), want (s.v.n, /)", state, ret)
	}
}

func TestOAuthCallbackLandsOnTheReturnPath(t *testing.T) {
	a, _ := fakeAuth(map[string]authnv1.UserInfo{"sha256~sso-token": {Username: "alice"}})
	o := fakeOAuth(t, "sha256~sso-token", a)
	for ret, want := range map[string]string{"/compute/vms/ns/web?tab=disks": "/compute/vms/ns/web?tab=disks", "//evil.example": "/"} {
		rec := httptest.NewRecorder()
		o.LoginRedirect(rec, httptest.NewRequest(http.MethodGet, "/api/auth/openshift?return="+url.QueryEscape(ret), nil))
		state := must(url.Parse(rec.Header().Get("Location"))).Query().Get("state")
		req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=abc&state="+url.QueryEscape(state), nil)
		for _, c := range rec.Result().Cookies() {
			req.AddCookie(c)
		}
		rec = httptest.NewRecorder()
		o.Callback(rec, req)
		if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != want {
			t.Errorf("return %q: callback landed on %d %q, want %q", ret, rec.Code, loc, want)
		}
	}
}

func TestOIDCCallbackLandsOnTheReturnPath(t *testing.T) {
	rig := newOIDCRig(t)
	for ret, want := range map[string]string{"/compute/vms/ns/web": "/compute/vms/ns/web", "https://evil.example": "/"} {
		rec := httptest.NewRecorder()
		rig.o.LoginRedirect(rec, httptest.NewRequest(http.MethodGet, "/api/auth/oidc?return="+url.QueryEscape(ret), nil))
		q := must(url.Parse(rec.Header().Get("Location"))).Query()
		rig.f.mu.Lock()
		rig.f.challenge, rig.f.nonce = q.Get("code_challenge"), q.Get("nonce")
		rig.f.mu.Unlock()
		req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=the-code&state="+url.QueryEscape(q.Get("state")), nil)
		for _, c := range rec.Result().Cookies() {
			req.AddCookie(c)
		}
		rec = httptest.NewRecorder()
		rig.o.Callback(rec, req)
		if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != want {
			t.Errorf("return %q: callback landed on %d %q, want %q", ret, rec.Code, loc, want)
		}
	}
}

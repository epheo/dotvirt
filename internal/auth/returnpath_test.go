package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestSafeReturnKeepsOnlySameOriginPages(t *testing.T) {
	for in, want := range map[string]string{
		"/compute/vms/ns/web":   "/compute/vms/ns/web",
		"/changes?pr=3#top":     "/changes?pr=3#top",
		"/":                     "/",
		"":                      "/",
		"compute":               "/",
		"//evil.example/x":      "/",
		"///evil.example":       "/",
		"//":                    "/",
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
		"/compute/":             "/compute/",
		"/./api/auth/oidc":      "/",
		"/x/../api/auth/oidc":   "/",
		"/%61pi/auth/oidc":      "/",
		"/%2Fevil.example":      "/",
		"/x%5Cy":                "/",
		"/x%0Ay":                "/",
		"/x/./y":                "/",
		"/x#/../api/auth/oidc":  "/",
		"/a/b#/../../api/me":    "/",
		"/changes#top":          "/changes#top",
		"/x?y=/../api":          "/x?y=/../api",
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

// Every tab of a restarted console redirects at once: each sign-in keeps its
// own state cookie, and finishing or failing one leaves the others intact.
func TestConcurrentSignInsKeepTheirOwnState(t *testing.T) {
	rig := newOIDCRig(t)
	type start struct {
		state, challenge, nonce string
		cookies                 []*http.Cookie
	}
	begin := func() start {
		rec := httptest.NewRecorder()
		rig.o.LoginRedirect(rec, httptest.NewRequest(http.MethodGet, "/api/auth/oidc", nil))
		q := must(url.Parse(rec.Header().Get("Location"))).Query()
		return start{q.Get("state"), q.Get("code_challenge"), q.Get("nonce"), rec.Result().Cookies()}
	}
	a, b := begin(), begin()
	if a.cookies[0].Name == b.cookies[0].Name {
		t.Fatalf("two sign-ins share the state cookie %q", a.cookies[0].Name)
	}
	jar := append(append([]*http.Cookie{}, a.cookies...), b.cookies...)
	finish := func(s start, code string) *httptest.ResponseRecorder {
		rig.f.mu.Lock()
		rig.f.challenge, rig.f.nonce = s.challenge, s.nonce
		rig.f.mu.Unlock()
		req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code="+code+"&state="+url.QueryEscape(s.state), nil)
		for _, c := range jar {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		rig.o.Callback(rec, req)
		return rec
	}
	// The second tab finishes first; its callback clears only its own cookie.
	rec := finish(b, "the-code")
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Fatalf("the second sign-in landed on %q", loc)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == a.cookies[0].Name {
			t.Fatalf("finishing one sign-in touched another's state cookie: %v", c)
		}
	}
	if loc := finish(a, "the-code").Header().Get("Location"); loc != "/" {
		t.Fatalf("the first sign-in landed on %q after the second finished", loc)
	}
}

// An unreachable OAuth server lands on the sign-in screen's failure button, not
// a bare error page the screen's own redirect would strand users on.
func TestOAuthStartWithoutDiscoveryLandsOnTheButton(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	o := &OAuth{saKube: kube, client: srv.Client()}
	rec := httptest.NewRecorder()
	o.LoginRedirect(rec, httptest.NewRequest(http.MethodGet, "/api/auth/openshift", nil))
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "/?sso_error=1" || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("start without discovery = %d %q, cookies %v", rec.Code, loc, rec.Result().Cookies())
	}
}

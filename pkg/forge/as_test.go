package forge

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A factory acting as one caller sends that caller's token alone, and leaves
// the factory it came from on its own.
func TestAsCarriesTheCallersTokenAlone(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"number":1,"state":"open"}`))
	}))
	defer srv.Close()

	bot := NewFactory(srv.URL, StaticToken("bot-token"), false, "")
	alice := bot.As("alice-token")
	if _, err := alice.For("https://elsewhere.example/o/r.git").PR(1); err != nil {
		t.Fatal(err)
	}
	if _, err := bot.For("https://elsewhere.example/o/r.git").PR(1); err != nil {
		t.Fatal(err)
	}
	if want := []string{"token alice-token", "token bot-token"}; len(seen) != 2 || seen[0] != want[0] || seen[1] != want[1] {
		t.Fatalf("Authorization sent = %q, want %q", seen, want)
	}
	if (*Factory)(nil).As("x") != nil {
		t.Fatal("no forge acts as no one")
	}
}

// The clone URL is under the factory's base whatever host the repo URL named:
// a token goes nowhere else.
func TestCloneURLIsUnderTheBase(t *testing.T) {
	f := NewFactory("https://forge.example/git/", StaticToken("t"), false, "")
	for _, repo := range []string{
		"https://forge.example/git/dotvirt/team-a.git",
		"http://forge.ns.svc:3000/dotvirt/team-a",
		"https://evil.example/dotvirt/team-a.git",
		"dotvirt/team-a.git",
	} {
		if got, want := f.For(repo).CloneURL(), "https://forge.example/git/dotvirt/team-a.git"; got != want {
			t.Errorf("CloneURL(%q) = %q, want %q", repo, got, want)
		}
	}
	if got := f.For("https://forge.example/dotvirt/team-a.git").FullName(); got != "dotvirt/team-a" {
		t.Errorf("FullName = %q", got)
	}
}

func TestCanPush(t *testing.T) {
	cases := []struct {
		status  int
		body    string
		want    bool
		refused int
	}{
		{http.StatusOK, `{"permissions":{"admin":false,"push":true,"pull":true}}`, true, 0},
		{http.StatusOK, `{"permissions":{"admin":false,"push":false,"pull":true}}`, false, 0},
		{http.StatusOK, `{}`, false, 0},
		// A private repo the token cannot see.
		{http.StatusNotFound, `{"message":"not found"}`, false, 0},
		{http.StatusUnauthorized, `{"message":"token is expired"}`, false, http.StatusUnauthorized},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/api/v1/repos/dotvirt/team-a" {
				t.Errorf("probe sent %s %s", r.Method, r.URL.Path)
			}
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(c.body))
		}))
		got, err := NewFactory(srv.URL, StaticToken("t"), false, "").For("dotvirt/team-a.git").CanPush()
		srv.Close()
		var se *StatusError
		switch {
		case c.refused != 0 && (!errors.As(err, &se) || se.Status != c.refused):
			t.Errorf("%d: want the forge's %d, got %v", c.status, c.refused, err)
		case c.refused == 0 && err != nil:
			t.Errorf("%d: %v", c.status, err)
		case got != c.want:
			t.Errorf("%d %s: CanPush = %v, want %v", c.status, c.body, got, c.want)
		}
	}
}

// redirecting answers the base's first request with a redirect to location
// and records every request the client sends, where and with what token.
type redirecting struct {
	location string
	mu       sync.Mutex
	sent     []string // "host path Authorization"
}

func (rt *redirecting) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	first := len(rt.sent) == 0
	rt.sent = append(rt.sent, r.URL.Host+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
	rt.mu.Unlock()
	h := http.Header{}
	status := http.StatusOK
	if first {
		status = http.StatusFound
		h.Set("Location", rt.location)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(`{"number":1}`)), Request: r}, nil
}

// A redirect off the forge's origin is refused before anything is sent
// there: a subdomain, another port or scheme, another host. One within the
// origin is followed.
func TestForgeRedirectsStayOnTheOrigin(t *testing.T) {
	for location, follows := range map[string]bool{
		"https://sub.forge.example/api/v1/repos/o/r/pulls/1":  false,
		"https://forge.example:8443/api/v1/repos/o/r/pulls/1": false,
		"http://forge.example/api/v1/repos/o/r/pulls/1":       false,
		"https://elsewhere.example/api/v1/repos/o/r/pulls/1":  false,
		"https://FORGE.example:443/api/v1/repos/o/r/pulls/2":  true,
		"/api/v1/repos/o/r/pulls/3":                           true,
	} {
		for name, f := range map[string]*Factory{
			"console": NewFactory("https://forge.example", StaticToken("bot-token"), false, ""),
			"user":    NewFactory("https://forge.example", StaticToken(""), false, "").As("user-token"),
		} {
			rt := &redirecting{location: location}
			f.http.Transport = rt
			_, err := f.For("o/r.git").PR(1)
			if follows {
				if err != nil || len(rt.sent) != 2 || !strings.HasPrefix(strings.ToLower(rt.sent[1]), "forge.example") {
					t.Errorf("%s, %s: %v, sent %q; want the redirect within the origin followed", name, location, err, rt.sent)
				}
				continue
			}
			if err == nil || !strings.Contains(err.Error(), "another origin") {
				t.Errorf("%s, %s: %v, want the redirect refused", name, location, err)
			}
			if len(rt.sent) != 1 {
				t.Errorf("%s, %s: sent %q, want no request, so no token, past the forge", name, location, rt.sent)
			}
		}
	}
}

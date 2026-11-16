package api

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/epheo/dotvirt/internal/auth"
)

// forgeSessionOf reads GET /api/forge/session with client's session.
func forgeSessionOf(t *testing.T, client *http.Client, base string) forgeSession {
	t.Helper()
	resp, err := client.Get(base + "/api/forge/session")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out forgeSession
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("forge session: %d: %v", resp.StatusCode, err)
	}
	return out
}

// A sign-out forgets the session's forge connection, wired as main wires it.
func TestForgeSessionAndSignOut(t *testing.T) {
	fc := newFakeCluster(t)
	s := fc.server(nil, Config{})
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		tr := action.(k8stesting.CreateAction).GetObject().(*authnv1.TokenReview).DeepCopy()
		tr.Status = authnv1.TokenReviewStatus{Authenticated: tr.Spec.Token == tenantToken, User: authnv1.UserInfo{Username: "tenant"}}
		return true, tr, nil
	})
	s.auth = auth.New(kube, []byte("0123456789abcdef0123456789abcdef"))
	s.delegated = auth.NewForge(auth.ForgeConfig{
		ClientID: "dotvirt", ClientSecret: "s3cret",
		AuthorizeURL: "https://forge.example/login/oauth/authorize",
		TokenURL:     "https://forge.example/login/oauth/access_token",
		APIURL:       "https://forge.example",
		RedirectURL:  "https://console.example/api/forge/callback",
	}, s.auth)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := client.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"token":"`+tenantToken+`"}`))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %v %v", resp, err)
	}
	if got := forgeSessionOf(t, client, srv.URL); !got.Delegated || got.Connected || got.Asked {
		t.Fatalf("before a connect: %+v", got)
	}
	resp, err = client.Get(srv.URL + "/api/forge/connect?return=/storage")
	if err != nil || resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), "https://forge.example/login/oauth/authorize?") {
		t.Fatalf("connect: %v %v", resp, err)
	}
	if got := forgeSessionOf(t, client, srv.URL); !got.Asked {
		t.Fatalf("after a connect started: %+v, want asked", got)
	}
	resp, err = client.Post(srv.URL+"/api/logout", "", nil)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %v %v", resp, err)
	}
	if st := s.delegated.Status(auth.Identity{Token: tenantToken}); st.Asked || st.Connected {
		t.Fatalf("sign-out kept the forge entry: %+v", st)
	}
}

// Without delegation the console says so and mounts no connect.
func TestForgeSessionWithoutDelegation(t *testing.T) {
	fc := newFakeCluster(t)
	s := fc.server(nil, Config{})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	if got := forgeSessionOf(t, http.DefaultClient, srv.URL); got.Delegated {
		t.Fatalf("delegation off: %+v", got)
	}
	resp, err := http.Get(srv.URL + "/api/forge/connect")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusFound {
		t.Fatal("connect mounted with delegation off")
	}
}

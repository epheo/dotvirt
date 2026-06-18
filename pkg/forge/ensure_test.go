package forge

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// EnsureRepo creates the repo (under its owner org, auto-initialised) only when it
// is absent, and is a no-op when it already exists.
func TestEnsureRepoCreatesWhenAbsent(t *testing.T) {
	var posted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/dotvirt/platform":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/dotvirt/repos":
			posted = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := NewFactory(srv.URL, "tok", false).For("http://forge/dotvirt/platform.git")
	created, err := c.EnsureRepo()
	if err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	if !created || !posted {
		t.Errorf("expected a create; created=%v posted=%v", created, posted)
	}
}

// EnsureOrgWebhook registers a single org-level hook (covering all repos) when none
// targets the URL yet.
func TestEnsureOrgWebhookRegistersOnce(t *testing.T) {
	var posted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/dotvirt/hooks":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/dotvirt/hooks":
			posted = true
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := NewFactory(srv.URL, "tok", false).For("http://forge/dotvirt/platform.git")
	if err := c.EnsureOrgWebhook("https://argo/api/webhook", "s3cr3t"); err != nil {
		t.Fatalf("EnsureOrgWebhook: %v", err)
	}
	if !posted {
		t.Error("expected the org hook to be created")
	}
}

// MintToken authenticates with basic auth and returns the sha1 from the response.
func TestMintToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "dotvirt-bot" || p != "pw" {
			t.Errorf("expected basic auth dotvirt-bot:pw, got %q:%q ok=%v", u, p, ok)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/users/dotvirt-bot/tokens" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sha1":"abc123","scopes":["write:organization"]}`))
	}))
	defer srv.Close()

	tok, err := NewFactory(srv.URL, "ignored", false).MintToken("dotvirt-bot", "pw", "dotvirt-operator", []string{"write:organization", "write:repository"})
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if tok != "abc123" {
		t.Errorf("token = %q, want abc123", tok)
	}
}

func TestValidateToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/user" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		// "good" authenticates, anything else is rejected.
		if r.Header.Get("Authorization") == "token good" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"login":"dotvirt-bot"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	f := NewFactory(srv.URL, "ignored", false)
	if valid, err := f.ValidateToken("good"); err != nil || !valid {
		t.Errorf("ValidateToken(good) = (%v,%v), want (true,nil)", valid, err)
	}
	if valid, err := f.ValidateToken("stale"); err != nil || valid {
		t.Errorf("ValidateToken(stale) = (%v,%v), want (false,nil)", valid, err)
	}
}

// EnsureOrg creates the owner org when absent and is a no-op when it exists.
func TestEnsureOrgCreatesWhenAbsent(t *testing.T) {
	var posted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/dotvirt":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs":
			posted = true
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := NewFactory(srv.URL, "tok", false).For("http://forge/dotvirt/platform.git")
	if err := c.EnsureOrg(); err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}
	if !posted {
		t.Error("expected the org to be created")
	}
}

func TestEnsureRepoSkipsWhenPresent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/dotvirt/platform" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		t.Errorf("unexpected %s %s (must not create when the repo already exists)", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewFactory(srv.URL, "tok", false).For("http://forge/dotvirt/platform.git")
	created, err := c.EnsureRepo()
	if err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	if created {
		t.Error("expected no create when the repo already exists")
	}
}

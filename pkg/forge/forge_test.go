package forge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOwnerRepo(t *testing.T) {
	cases := []struct {
		url         string
		owner, repo string
		ok          bool
	}{
		{"https://forge.example/dotvirt/team-a.git", "dotvirt", "team-a", true},
		{"https://forge.example/dotvirt/team-a", "dotvirt", "team-a", true},
		{"https://forge.example/dotvirt/team-a/", "dotvirt", "team-a", true},
		{"http://forgejo-http.forgejo.svc:3000/dotvirt/vmrepo.git", "dotvirt", "vmrepo", true},
		// Query string must not leak into the repo name (fails closed otherwise).
		{"https://forge.example/dotvirt/team-a.git?ref=main", "dotvirt", "team-a", true},
		{"https://forge.example/dotvirt/team-a?x=1#frag", "dotvirt", "team-a", true},
		// Nested groups: last two segments win.
		{"https://forge.example/org/sub/team-a.git", "sub", "team-a", true},
		// Unparseable → fail closed.
		{"https://forge.example/onlyone", "", "", false},
		{"not-a-url", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		owner, repo, ok := ownerRepo(c.url)
		if ok != c.ok || owner != c.owner || repo != c.repo {
			t.Errorf("ownerRepo(%q) = (%q,%q,%v), want (%q,%q,%v)", c.url, owner, repo, ok, c.owner, c.repo, c.ok)
		}
	}
}

func TestOwnerPrefixURL(t *testing.T) {
	cases := []struct {
		url, want string
	}{
		// The scheme's "//" must survive — path.Dir would collapse it to "https:/",
		// yielding a prefix Argo never longest-prefix-matches (the original bug).
		{"https://forge.example/dotvirt/platform.git", "https://forge.example/dotvirt"},
		{"https://forge.example/dotvirt/platform", "https://forge.example/dotvirt"},
		{"https://forge.example/dotvirt/platform/", "https://forge.example/dotvirt"},
		{"http://forgejo-http.forgejo.svc:3000/dotvirt/platform.git", "http://forgejo-http.forgejo.svc:3000/dotvirt"},
		// Nested groups: only the last segment is stripped.
		{"https://forge.example/org/sub/platform.git", "https://forge.example/org/sub"},
		// No repo segment to strip → returned unchanged.
		{"https://forge.example", "https://forge.example"},
		{"https://forge.example/", "https://forge.example/"},
	}
	for _, c := range cases {
		if got := OwnerPrefixURL(c.url); got != c.want {
			t.Errorf("OwnerPrefixURL(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

// testClient points a Client at an httptest server with the standard owner/repo.
func testClient(srvURL string) *Client {
	return NewFactory(srvURL, "tok", false).For(srvURL + "/dotvirt/team-a.git")
}

func TestFindPRAcrossStates(t *testing.T) {
	const head = "dotvirt/proposed/alice/team-a-abc123"

	t.Run("closed only", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, []PR{{Number: 4, State: "closed", HTMLURL: "u4", Head: refHead(head)}})
		}))
		defer srv.Close()
		pr, ok, err := testClient(srv.URL).FindPR(head, "main")
		if err != nil || !ok {
			t.Fatalf("FindPR: ok=%v err=%v", ok, err)
		}
		if pr.Number != 4 || pr.State != "closed" {
			t.Fatalf("got %+v, want closed #4", pr)
		}
	})

	t.Run("open wins over closed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, []PR{
				{Number: 4, State: "closed", HTMLURL: "u4", Head: refHead(head)},
				{Number: 7, State: "open", HTMLURL: "u7", Head: refHead(head)},
			})
		}))
		defer srv.Close()
		pr, ok, err := testClient(srv.URL).FindPR(head, "main")
		if err != nil || !ok || pr.Number != 7 {
			t.Fatalf("got (#%d, ok=%v, err=%v), want open #7", pr.Number, ok, err)
		}
	})

	t.Run("non-matching head ref ignored", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, []PR{{Number: 9, State: "open", Head: refHead("someone-elses-branch")}})
		}))
		defer srv.Close()
		_, ok, err := testClient(srv.URL).FindPR(head, "main")
		if err != nil || ok {
			t.Fatalf("want ok=false, got ok=%v err=%v", ok, err)
		}
	})
}

func TestReopenPR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		if want := "/api/v1/repos/dotvirt/team-a/pulls/4"; r.URL.Path != want {
			t.Errorf("path = %s, want %s", r.URL.Path, want)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["state"] != "open" {
			t.Errorf("body = %v, want state=open", body)
		}
		writeJSON(t, w, PR{Number: 4, State: "open", HTMLURL: "u4"})
	}))
	defer srv.Close()

	pr, err := testClient(srv.URL).ReopenPR(4)
	if err != nil {
		t.Fatalf("ReopenPR: %v", err)
	}
	if pr.State != "open" || pr.Number != 4 {
		t.Fatalf("got %+v, want open #4", pr)
	}
}

func TestCreatePRConflictPreservesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "pull request already exists", http.StatusConflict)
	}))
	defer srv.Close()

	_, err := testClient(srv.URL).CreatePR("t", "b", "head", "main")
	if err == nil {
		t.Fatal("want error on 409")
	}
	// The 409 status must survive in the error string (log format relies on it).
	if !strings.Contains(err.Error(), "409") {
		t.Fatalf("error %q does not mention 409", err)
	}
}

func refHead(ref string) struct {
	Ref string `json:"ref"`
} {
	return struct {
		Ref string `json:"ref"`
	}{Ref: ref}
}

func writeJSON(t *testing.T, w io.Writer, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

// EnsureWebhook creates the hook only when no hook for the target URL exists, and
// re-asserts the secret on an existing hook in place (PATCH), never recreating it.
func TestEnsureWebhookIdempotent(t *testing.T) {
	posts, patches := 0, 0
	existing := `[]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/hooks"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, existing)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/hooks"):
			posts++
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":1}`)
		case r.Method == "PATCH" && strings.HasSuffix(r.URL.Path, "/hooks/1"):
			patches++
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := NewFactory(srv.URL, "tok", false).For("https://forge/o/r.git")
	if err := c.EnsureWebhook("https://dotvirt/api/webhooks/forge", "s3cret"); err != nil {
		t.Fatalf("EnsureWebhook (create): %v", err)
	}
	if posts != 1 {
		t.Fatalf("want 1 create, got %d", posts)
	}

	existing = `[{"id":1,"config":{"url":"https://dotvirt/api/webhooks/forge"}}]`
	if err := c.EnsureWebhook("https://dotvirt/api/webhooks/forge", "s3cret"); err != nil {
		t.Fatalf("EnsureWebhook (existing): %v", err)
	}
	if posts != 1 {
		t.Fatalf("existing hook must not be recreated; got %d creates", posts)
	}
	if patches != 1 {
		t.Fatalf("existing hook must be reconciled in place; got %d patches", patches)
	}
}

func TestNormalizeRepoURL(t *testing.T) {
	// Every spelling of the same repo must canonicalize to one key, so a push
	// webhook reliably finds the repo's poller and its managing ArgoCD Application.
	want := "https://forge.example/org/team-a"
	for _, in := range []string{
		"https://forge.example/org/team-a.git",
		"https://forge.example/org/team-a",
		"https://forge.example/org/team-a/",
		"https://forge.example/org/team-a.GIT", // mixed-case suffix must still strip
		"  https://Forge.Example/org/team-a.git  ",
	} {
		if got := NormalizeRepoURL(in); got != want {
			t.Errorf("NormalizeRepoURL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := NormalizeRepoURL(""); got != "" {
		t.Errorf("NormalizeRepoURL(\"\") = %q, want empty", got)
	}
}

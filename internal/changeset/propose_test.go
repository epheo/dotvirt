package changeset

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/forge"
)

// proposeFixture wires a Coordinator over a seeded bare repo and a forge Factory
// pointed at an httptest server scripted by routes. The bare repo path doubles as
// proj.Repo: forge.For parses its last two segments as owner/repo, while the API
// calls hit srv regardless of host — so routes match on the path suffix.
type proposeFixture struct {
	c    *Coordinator
	proj project.ProjectInfo
	id   auth.Identity
}

// route returns the response for a (method, path-suffix) pair, or 0 to fall through.
type route func(method, path string) (status int, body string, matched bool)

func newProposeFixture(t *testing.T, routes ...route) *proposeFixture {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, rt := range routes {
			if status, body, ok := rt(r.Method, r.URL.Path); ok {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
				return
			}
		}
		t.Errorf("unhandled forge call: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unhandled", http.StatusNotImplemented)
	}))
	t.Cleanup(srv.Close)

	store, err := draft.Open(t.TempDir())
	if err != nil {
		t.Fatalf("draft.Open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repos := git.NewRepoSet(ctx, "", "", false, make(chan struct{}, 1), nil, time.Hour)
	ff := forge.NewFactory(srv.URL, "tok", false)
	c := New(store, repos, ff, nil, "main", "dotvirt/proposed", "running")

	return &proposeFixture{
		c:    c,
		proj: project.ProjectInfo{Name: "p", Repo: seedBare(t)},
		id:   auth.Identity{Username: "alice"},
	}
}

func when(method, suffix string, status int, body string) route {
	return func(m, path string) (int, string, bool) {
		if m == method && strings.HasPrefix(path, "/api/v1/repos/") && strings.HasSuffix(strings.TrimRight(path, "/"), suffix) {
			return status, body, true
		}
		return 0, "", false
	}
}

func (f *proposeFixture) stageAndPropose(t *testing.T) model.ProposeResult {
	t.Helper()
	req := model.EditRequest{SourceFile: "web.yaml", SetLabels: map[string]string{"env": "prod"}}
	if _, err := f.c.StageEdit(f.id, f.proj, "alpha", "web", req); err != nil {
		t.Fatalf("StageEdit: %v", err)
	}
	out, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{Title: "edit web"})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	return out
}

func (f *proposeFixture) draftCount(t *testing.T) int {
	t.Helper()
	entries, err := f.c.store.List(f.id.Username, f.proj.Name)
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	return len(entries)
}

// A fresh propose with no existing PR creates one; its URL is returned and the
// draft is cleared.
func TestProposeCreatesPR(t *testing.T) {
	f := newProposeFixture(t,
		when("POST", "/pulls", http.StatusCreated, `{"number":1,"state":"open","html_url":"http://forge/pulls/1"}`),
	)
	out := f.stageAndPropose(t)
	if out.PRURL != "http://forge/pulls/1" || out.Existing {
		t.Fatalf("got %+v, want fresh PR URL", out)
	}
	if n := f.draftCount(t); n != 0 {
		t.Fatalf("draft not cleared: %d entries", n)
	}
}

// A closed-not-merged PR on the reused branch is reopened, and its URL returned.
func TestProposeReopensClosedPR(t *testing.T) {
	f := newProposeFixture(t,
		when("POST", "/pulls", http.StatusConflict, "pull request already exists"),
		// GET /pulls?... → the branch's closed PR. Head ref must match the
		// per-(user,project) branch the coordinator builds.
		whenList("GET", "/pulls", http.StatusOK, "closed", false),
		when("PATCH", "/pulls/4", http.StatusOK, `{"number":4,"state":"open","html_url":"http://forge/pulls/4"}`),
	)
	out := f.stageAndPropose(t)
	if out.PRURL != "http://forge/pulls/4" || !out.Existing {
		t.Fatalf("got %+v, want reopened existing PR #4", out)
	}
	if n := f.draftCount(t); n != 0 {
		t.Fatalf("draft not cleared: %d entries", n)
	}
}

// A merged PR is not reopened; the user gets a compare URL and keeps the draft.
func TestProposeMergedFallsBackToCompare(t *testing.T) {
	f := newProposeFixture(t,
		when("POST", "/pulls", http.StatusConflict, "pull request already exists"),
		whenList("GET", "/pulls", http.StatusOK, "closed", true),
	)
	out := f.stageAndPropose(t)
	if out.PRURL != "" {
		t.Fatalf("got PRURL %q, want none for merged PR", out.PRURL)
	}
	if out.CompareURL == "" {
		t.Fatalf("want compare URL fallback, got %+v", out)
	}
	if n := f.draftCount(t); n != 1 {
		t.Fatalf("draft should be kept on fallback: %d entries", n)
	}
}

// A genuine CreatePR failure with no findable PR falls back to compare URL and
// keeps the draft for retry.
func TestProposeRealFailureFallsBackToCompare(t *testing.T) {
	f := newProposeFixture(t,
		when("POST", "/pulls", http.StatusInternalServerError, "boom"),
		when("GET", "/pulls", http.StatusOK, "[]"),
	)
	out := f.stageAndPropose(t)
	if out.PRURL != "" || out.CompareURL == "" {
		t.Fatalf("got %+v, want compare-URL fallback", out)
	}
	if n := f.draftCount(t); n != 1 {
		t.Fatalf("draft should be kept on failure: %d entries", n)
	}
}

// whenList answers a GET /pulls listing with a single PR whose head ref matches
// the coordinator's per-(user,project) branch, in the given state/merged combo.
func whenList(method, suffix string, status int, state string, merged bool) route {
	return func(m, path string) (int, string, bool) {
		if m != method || !strings.HasSuffix(strings.TrimRight(path, "/"), suffix) {
			return 0, "", false
		}
		head := proposedBranchFor("alice", "p")
		body := `[{"number":4,"state":"` + state + `","merged":` + boolStr(merged) +
			`,"html_url":"http://forge/pulls/4","head":{"ref":"` + head + `"}}]`
		return status, body, true
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// proposedBranchFor mirrors Coordinator.proposedBranch for the default prefix, so
// the test's stub PR head matches what Propose actually pushes.
func proposedBranchFor(user, proj string) string {
	c := &Coordinator{proposed: "dotvirt/proposed"}
	return c.proposedBranch(user, proj)
}

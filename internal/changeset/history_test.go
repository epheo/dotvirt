package changeset

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
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

func sizedVM(name string, cores int) []byte {
	return []byte("apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: " + name +
		"\n  namespace: alpha\nspec:\n  runStrategy: Always\n  template:\n    spec:\n      domain:\n        cpu:\n          cores: " +
		string(rune('0'+cores)) + "\n        memory:\n          guest: 4Gi\n")
}

// seedMerged builds main with alpha/web (2 cores), then merges a PR-style
// branch that resizes web to 4 cores and adds alpha/db, under the subject
// Forgejo writes. Returns the bare repo, the work clone and the merge hash.
func seedMerged(t *testing.T) (bare, work, mergeHash string) {
	t.Helper()
	bare, work = seedWork(t, map[string][]byte{"alpha/web.yaml": sizedVM("web", 2)})
	gitRun(t, work, "checkout", "-qb", "feature")
	writeWorkFile(t, work, "alpha/web.yaml", sizedVM("web", 4))
	writeWorkFile(t, work, "alpha/db.yaml", sizedVM("db", 1))
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-qm", "resize")
	gitRun(t, work, "checkout", "-q", "main")
	gitRun(t, work, "merge", "-q", "--no-ff", "-m",
		"Merge pull request 'Resize web' (#12) from dotvirt/proposed/alice/p-1a2b into main", "feature")
	gitRun(t, work, "push", "-q", "origin", "main")
	return bare, work, gitOut(t, work, "rev-parse", "HEAD")
}

func gitOut(t *testing.T, wd string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = wd
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// A merged PR reviews as the same semantic items a draft renders, named by the
// PR it merged, with the merge commit as the unit.
func TestCommitRendersMergedPR(t *testing.T) {
	bare, _, hash := seedMerged(t)
	c := newTestCoordinator(t)
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	commits, err := c.History(proj, 25)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if commits[0].Hash != hash || commits[0].Title != "Resize web" || commits[0].PRNumber != 12 {
		t.Fatalf("merge row should be named by its PR: %+v", commits[0])
	}
	if commits[0].PRURL != "" {
		t.Errorf("no forge configured, so no PR link; got %q", commits[0].PRURL)
	}

	d, err := c.Commit(proj, hash)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if d.Commit.Title != "Resize web" || d.Reverted || d.RevertWarning != "" {
		t.Fatalf("unexpected detail: %+v", d)
	}
	if len(d.Items) != 2 {
		t.Fatalf("want an edit of web and a create of db, got %+v", d.Items)
	}
	db, web := d.Items[0], d.Items[1]
	if db.Kind != "create" || db.Name != "db" || db.Namespace != "alpha" || !strings.Contains(db.YAML, "name: db") {
		t.Errorf("db create: %+v", db)
	}
	if web.Kind != "edit" || web.Name != "web" || len(web.Changes) != 1 ||
		web.Changes[0].Field != "CPU" || web.Changes[0].From != "2 vCPU" || web.Changes[0].To != "4 vCPU" {
		t.Errorf("web edit should be the CPU field diff: %+v", web)
	}
	if _, err := c.Commit(proj, "0123456789abcdef0123456789abcdef01234567"); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("unknown hash should be ErrNotFound, got %v", err)
	}
}

// A later commit to a touched file changes what a revert does: the review must
// say so before the click, and once main is back to the pre-commit content a
// revert has nothing left to do.
func TestCommitRevertStateTracksMain(t *testing.T) {
	bare, work, hash := seedMerged(t)
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	writeWorkFile(t, work, "alpha/web.yaml", sizedVM("web", 8))
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-qm", "bump again")
	gitRun(t, work, "push", "-q", "origin", "main")
	// A coordinator opened after each push mirrors it; the poll path is async.
	c := newTestCoordinator(t)

	d, err := c.Commit(proj, hash)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !strings.Contains(d.RevertWarning, "alpha/web.yaml") || d.Reverted {
		t.Fatalf("want a later-change warning naming web.yaml, got %+v", d)
	}

	writeWorkFile(t, work, "alpha/web.yaml", sizedVM("web", 2))
	gitRun(t, work, "rm", "-q", "alpha/db.yaml")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-qm", "manual undo")
	gitRun(t, work, "push", "-q", "origin", "main")
	c = newTestCoordinator(t)

	d, err = c.Commit(proj, hash)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !d.Reverted || d.RevertWarning != "" {
		t.Fatalf("main carries the pre-merge content, so the change reads as reverted: %+v", d)
	}
	if _, err := c.Revert(auth.Identity{Username: "alice"}, proj, hash); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("reverting an already-reverted change must be a classified refusal, got %v", err)
	}
}

// Reverting a merge opens a PR that undoes the whole merged PR: web back to its
// pre-merge size, db gone, named after the PR and carrying the semantic diff.
func TestRevertMergeOpensPR(t *testing.T) {
	bare, _, hash := seedMerged(t)
	var got struct {
		Title, Body, Head, Base string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/pulls") {
			t.Errorf("unexpected forge call %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotImplemented)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":13,"state":"open","html_url":"http://forge/pulls/13"}`))
	}))
	t.Cleanup(srv.Close)
	store, err := draft.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := New(store, git.NewRepoSet(ctx, "", nil, true, nil, time.Hour), forge.NewFactory(srv.URL, "tok", false), nil, nil, nil, "main", "dotvirt/proposed")
	proj := project.ProjectInfo{Name: "p", Repo: bare}

	out, err := c.Revert(auth.Identity{Username: "alice"}, proj, hash)
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if out.PRNumber != 13 || out.PRURL == "" || !out.Pushed {
		t.Fatalf("want an opened PR, got %+v", out)
	}
	if got.Title != `Revert "Resize web" (#12)` || got.Base != "main" || got.Head != out.Branch {
		t.Errorf("PR request: %+v", got)
	}
	for _, want := range []string{"Reverts commit " + hash[:8], "## Changes", "**edit alpha/web**", "CPU: 4 vCPU -> 2 vCPU", "**delete alpha/db**"} {
		if !strings.Contains(got.Body, want) {
			t.Errorf("PR body lacks %q:\n%s", want, got.Body)
		}
	}

	work := t.TempDir()
	gitRun(t, work, "clone", "-q", "-b", out.Branch, bare, ".")
	if content := gitOut(t, work, "show", "HEAD:alpha/web.yaml"); !strings.Contains(content, "cores: 2") {
		t.Errorf("web.yaml should be back to 2 cores on the revert branch:\n%s", content)
	}
	if gitOut(t, work, "ls-files", "alpha/db.yaml") != "" {
		t.Error("db.yaml was added by the merge, so the revert branch must not carry it")
	}
}

// A cluster-scoped object reviews by its bare name: the directory it lives in
// is not a namespace, and the review must not invent one.
func TestCommitItemsClusterScopedName(t *testing.T) {
	ns := []byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: legacy-app\n  labels:\n    dotvirt.io/project: legacy-app\n")
	items := commitItems([]git.FileChange{{Path: "namespaces/legacy-app.yaml", After: ns}})
	if len(items) != 1 {
		t.Fatalf("want one item, got %+v", items)
	}
	it := items[0]
	if it.Namespace != "" || it.Name != "legacy-app" || it.Resource != "Namespace" || it.Kind != "create" {
		t.Errorf("item = %+v, want a bare-named Namespace create", it)
	}
	if it.Changes[0].To != "legacy-app" {
		t.Errorf("create change should name the object bare, got %+v", it.Changes[0])
	}
	del := commitItems([]git.FileChange{{Path: "namespaces/legacy-app.yaml", Before: ns}})
	if del[0].Kind != "delete" || del[0].Changes[0].From != "legacy-app" {
		t.Errorf("delete should name the object bare, got %+v", del[0])
	}
}

// The open-PR lane carries the draft's PR and every revert the user opened,
// found by their branch prefix among the open PRs, with one branch-rule read.
func TestOpenProposalsIncludesReverts(t *testing.T) {
	bare, _, hash := seedMerged(t)
	revertHead := (&Coordinator{proposed: "dotvirt/proposed"}).revertBranch("alice", "p", hash)
	draftHead := proposedBranchFor("alice", "p")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pulls") && q.Get("state") == "all":
			_, _ = w.Write([]byte(`[{"number":4,"state":"open","html_url":"http://forge/pulls/4","title":"edit web","base":{"ref":"main"},"head":{"ref":"` + draftHead + `","sha":"aaa"}}]`))
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pulls") && q.Get("state") == "open":
			_, _ = w.Write([]byte(`[{"number":4,"state":"open","html_url":"http://forge/pulls/4","title":"edit web","base":{"ref":"main"},"head":{"ref":"` + draftHead + `","sha":"aaa"}},` +
				`{"number":9,"state":"open","html_url":"http://forge/pulls/9","title":"Revert \"Resize web\" (#12)","base":{"ref":"main"},"head":{"ref":"` + revertHead + `","sha":"bbb"}},` +
				`{"number":10,"state":"open","html_url":"http://forge/pulls/10","title":"bob's revert","base":{"ref":"main"},"head":{"ref":"dotvirt/proposed/revert/bob/p-` + hash[:8] + `","sha":"ccc"}},` +
				`{"number":11,"state":"open","html_url":"http://forge/pulls/11","title":"human PR","base":{"ref":"main"},"head":{"ref":"feature","sha":"ddd"}}]`))
		case strings.HasSuffix(r.URL.Path, "/branch_protections"):
			_, _ = w.Write([]byte(`[{"branch_name":"main","required_approvals":1}]`))
		case strings.Contains(r.URL.Path, "/reviews"):
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(r.URL.Path, "/status"):
			_, _ = w.Write([]byte(`{"state":"success","total_count":1}`))
		default:
			t.Errorf("unexpected forge call %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected", http.StatusNotImplemented)
		}
	}))
	t.Cleanup(srv.Close)
	store, err := draft.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := New(store, git.NewRepoSet(ctx, "", nil, false, nil, time.Hour), forge.NewFactory(srv.URL, "tok", false), nil, nil, nil, "main", "dotvirt/proposed")

	prs, err := c.OpenProposals(auth.Identity{Username: "alice"}, project.ProjectInfo{Name: "p", Repo: bare})
	if err != nil {
		t.Fatalf("OpenProposals: %v", err)
	}
	if len(prs) != 2 || prs[0].PRNumber != 4 || prs[1].PRNumber != 9 {
		t.Fatalf("want the draft PR #4 and alice's revert #9 only, got %+v", prs)
	}
	for _, p := range prs {
		if p.RequiredApprovals != 1 || p.Checks != "success" {
			t.Errorf("review state missing on %+v", p)
		}
	}
}

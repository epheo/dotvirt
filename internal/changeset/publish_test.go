package changeset

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/forge"
)

// forgePRs is a stateful PR plane for propose scenarios: the list, reads by
// number, creates and edits all act on one table, the way Forgejo does.
type forgePRs struct {
	f    *proposeFixture
	head string

	mu  sync.Mutex
	prs []forge.PR
	// onRead, when set, runs on every read by number before it answers.
	onRead func(pr *forge.PR, reads int)
	reads  map[int]int
}

func newForgePRs() *forgePRs {
	return &forgePRs{head: proposedBranchFor("alice", "p"), reads: map[int]int{}}
}

func (p *forgePRs) route(m, path string) (int, string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	list := strings.HasSuffix(path, "/pulls")
	if list && m == "GET" {
		return p.json(http.StatusOK, p.prs)
	}
	if list && m == "POST" {
		var in struct{ Title, Body string }
		_ = json.Unmarshal([]byte(p.f.lastBody()), &in)
		pr := forge.PR{Number: len(p.prs) + 1, State: "open", Title: in.Title, Body: in.Body}
		pr.HTMLURL = fmt.Sprintf("http://forge/pulls/%d", pr.Number)
		pr.Head.Ref, pr.Base.Ref = p.head, "main"
		p.prs = append(p.prs, pr)
		return p.json(http.StatusCreated, pr)
	}
	i := strings.LastIndex(path, "/pulls/")
	if i < 0 {
		return 0, "", false
	}
	n, err := strconv.Atoi(path[i+len("/pulls/"):])
	if err != nil || n < 1 || n > len(p.prs) {
		return 0, "", false
	}
	pr := &p.prs[n-1]
	switch m {
	case "GET":
		p.reads[n]++
		if p.onRead != nil {
			p.onRead(pr, p.reads[n])
		}
		return p.json(http.StatusOK, *pr)
	case "PATCH":
		var edit forge.PREdit
		_ = json.Unmarshal([]byte(p.f.lastBody()), &edit)
		if edit.Title != "" {
			pr.Title = edit.Title
		}
		if edit.Body != "" {
			pr.Body = edit.Body
		}
		if edit.State != "" {
			pr.State = edit.State
		}
		return p.json(http.StatusOK, *pr)
	}
	return 0, "", false
}

func (p *forgePRs) json(status int, v any) (int, string, bool) {
	b, _ := json.Marshal(v)
	return status, string(b), true
}

func (p *forgePRs) pr(n int) forge.PR {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.prs[n-1]
}

// newPRFixture is a propose fixture over a forgePRs plane.
func newPRFixture(t *testing.T) (*proposeFixture, *forgePRs) {
	t.Helper()
	prs := newForgePRs()
	f := newProposeFixture(t, prs.route)
	prs.f = f
	return f, prs
}

func (f *proposeFixture) lastBody() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent[len(f.sent)-1].body
}

func (f *proposeFixture) stageLabel(t *testing.T, key, value string) model.DraftView {
	t.Helper()
	req := model.EditRequest{SourceFile: "web.yaml", VMEdit: model.VMEdit{SetLabels: map[string]string{key: value}}}
	view, err := f.c.StageEdit(f.id, f.proj, "alpha", "web", req)
	if err != nil {
		t.Fatalf("StageEdit: %v", err)
	}
	return view
}

// commitMain lands content at path on main out of band, as another user's
// merge would, and waits for the mirror to see it.
func (f *proposeFixture) commitMain(t *testing.T, path, content string) {
	t.Helper()
	work := t.TempDir()
	gitRun(t, work, "clone", "-q", f.proj.Repo, ".")
	writeWorkFile(t, work, path, []byte(content))
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-qm", "out of band: "+path)
	gitRun(t, work, "push", "-q", "origin", "main")
	want := revParse(t, f.proj.Repo, "main")
	read, err := f.c.read(f.proj)
	if err != nil {
		t.Fatal(err)
	}
	f.repos.Poke(f.proj.Repo)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if commits, err := read.History("main", 1); err == nil && len(commits) == 1 && commits[0].Hash == want {
			return
		}
	}
	t.Fatal("the mirror never saw the out-of-band commit")
}

func (f *proposeFixture) show(t *testing.T, rev string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", f.proj.Repo, "show", rev).Output()
	if err != nil {
		t.Fatalf("git show %s: %v", rev, err)
	}
	return string(out)
}

func (f *proposeFixture) proposal(t *testing.T) (draft.Proposal, bool) {
	t.Helper()
	p, ok, err := f.c.store.Proposal(f.id.Username, f.proj.Name)
	if err != nil {
		t.Fatal(err)
	}
	return p, ok
}

// Re-proposing while the branch's PR is open rebuilds it from current base
// with what the PR carries and the draft on top, under the head dotvirt
// pushed, and rewrites the PR to describe the whole under the user's message.
func TestReproposeRebuildsTheOpenPR(t *testing.T) {
	f, prs := newPRFixture(t)
	f.stageLabel(t, "env", "prod")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{Title: "edit web", Message: "Why: launch."}); err != nil {
		t.Fatalf("first Propose: %v", err)
	}
	if rec, ok := f.proposal(t); !ok || rec.PR != 1 || rec.Head != revParse(t, f.proj.Repo, prs.head) || len(rec.Entries) != 1 {
		t.Fatalf("the first propose must record PR #1 and its head, got %+v ok=%v", rec, ok)
	}

	view := f.stageLabel(t, "tier", "gold")
	out, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{Title: view.DefaultTitle})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if out.PRNumber != 1 || !out.Existing {
		t.Fatalf("got %+v, want the open PR #1", out)
	}
	if parent := revParse(t, f.proj.Repo, prs.head+"^"); parent != revParse(t, f.proj.Repo, "main") {
		t.Fatal("the branch must be rebuilt on base, not stacked on the old head")
	}
	file := f.show(t, prs.head+":web.yaml")
	if !strings.Contains(file, "env: prod") || !strings.Contains(file, "tier: gold") {
		t.Fatalf("the PR must carry both labels:\n%s", file)
	}
	pr := prs.pr(1)
	if pr.Title == view.DefaultTitle || !strings.Contains(pr.Title, "web") {
		t.Errorf("the title must describe the whole PR, not this draft alone: %q", pr.Title)
	}
	for _, want := range []string{"Why: launch.", "prod", "gold"} {
		if !strings.Contains(pr.Body, want) {
			t.Errorf("the body must carry %q:\n%s", want, pr.Body)
		}
	}
	rec, _ := f.proposal(t)
	if rec.Head != revParse(t, f.proj.Repo, prs.head) || len(rec.Entries) != 1 || rec.Entries[0].Edit.SetLabels["tier"] != "gold" {
		t.Fatalf("the record must hold the new head and the merged edit, got %+v", rec)
	}
	if n := f.draftCount(t); n != 0 {
		t.Fatalf("draft not cleared: %d entries", n)
	}
}

// Main moves after the PR is cut. The rebuild runs on the new main: a field
// main changed and the draft changes back is no no-op, and the PR keeps what
// it carried.
func TestReproposeRunsOnTheCurrentBase(t *testing.T) {
	f, prs := newPRFixture(t)
	f.stageLabel(t, "env", "prod")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatal(err)
	}
	f.commitMain(t, "web.yaml", "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: web\n  namespace: alpha\nspec:\n  runStrategy: Halted\n")

	on := "On"
	if _, err := f.c.StageEdit(f.id, f.proj, "alpha", "web", model.EditRequest{SourceFile: "web.yaml", VMEdit: model.VMEdit{Power: &on}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	file := f.show(t, prs.head+":web.yaml")
	if !strings.Contains(file, "runStrategy: Always") || !strings.Contains(file, "env: prod") {
		t.Fatalf("want web running again with the PR's label:\n%s", file)
	}
}

// A file the PR never touched moves on main after the PR is cut; a
// whole-file edit made on main's new version proposes cleanly - the pin is
// checked against main, never against the PR's older copy.
func TestReproposePinsAgainstTheCurrentBase(t *testing.T) {
	f, prs := newPRFixture(t)
	f.commitMain(t, "db.yaml", "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: db\n  namespace: alpha\nspec:\n  runStrategy: Always\n")
	f.stageLabel(t, "env", "prod")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatal(err)
	}
	f.commitMain(t, "db.yaml", "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: db\n  namespace: alpha\n  labels:\n    bob: x\nspec:\n  runStrategy: Always\n")

	alice := "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: db\n  namespace: alpha\n  labels:\n    bob: x\n    alice: y\nspec:\n  runStrategy: Always\n"
	if _, err := f.c.StageUpdateManifest(f.id, f.proj, "", "alpha", "db", model.UpdateManifestRequest{YAML: alice}); err != nil {
		t.Fatalf("StageUpdateManifest: %v", err)
	}
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatalf("a pin true on main must propose: %v", err)
	}
	if got := f.show(t, prs.head+":db.yaml"); got != alice {
		t.Fatalf("db.yaml = %q, want the edit", got)
	}
	if !strings.Contains(f.show(t, prs.head+":web.yaml"), "env: prod") {
		t.Fatal("the PR's earlier change must stay")
	}
}

// The PR merges, at its old head, while its branch is rebuilt. What it
// carried is in main now; the draft alone goes out as a new PR, rebuilt once
// more on the main that holds the merge, so the new PR never carries the
// merged change again (a later revert of it stays reverted).
func TestProposeAfterTheOpenPRMergedMeanwhile(t *testing.T) {
	f, prs := newPRFixture(t)
	f.stageLabel(t, "env", "prod")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatal(err)
	}
	old := revParse(t, f.proj.Repo, prs.head)
	prs.onRead = func(pr *forge.PR, reads int) {
		if pr.Number == 1 && reads == 2 {
			mergeInto(t, f.proj.Repo, old)
			pr.State, pr.Merged = "closed", true
		}
	}
	f.stageLabel(t, "tier", "gold")
	out, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if out.PRNumber != 2 || out.Existing {
		t.Fatalf("got %+v, want a new PR #2", out)
	}
	rec, ok := f.proposal(t)
	if !ok || rec.PR != 2 || len(rec.Entries) != 1 || rec.Entries[0].Edit.SetLabels["tier"] != "gold" || rec.Entries[0].Edit.SetLabels["env"] != "" {
		t.Fatalf("the new PR carries the draft alone, got %+v", rec)
	}
	if revParse(t, f.proj.Repo, prs.head+"^") != revParse(t, f.proj.Repo, "main") {
		t.Fatal("the new PR's branch must sit on the main that holds the merge")
	}
	if diff := gitDiff(t, f.proj.Repo, "main", prs.head); !strings.Contains(diff, "+    tier: gold") || strings.Contains(diff, "+    env") || strings.Contains(diff, "-    env") {
		t.Fatalf("the new PR must change tier alone:\n%s", diff)
	}
}

// mergeInto lands rev on main with a merge commit, as the forge merging a PR.
func mergeInto(t *testing.T, bare, rev string) {
	t.Helper()
	work := t.TempDir()
	gitRun(t, work, "clone", "-q", bare, ".")
	gitRun(t, work, "fetch", "-q", "origin", rev)
	gitRun(t, work, "merge", "-q", "--no-ff", "-m", "Merge PR", "FETCH_HEAD")
	gitRun(t, work, "push", "-q", "origin", "main")
}

func gitDiff(t *testing.T, repo, from, to string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "diff", from, to).Output()
	if err != nil {
		t.Fatalf("git diff: %v", err)
	}
	return string(out)
}

// The PR closes while its branch is rebuilt: the draft stays, the record goes,
// and the next propose reopens it with the draft alone.
func TestProposeAfterTheOpenPRClosedMeanwhile(t *testing.T) {
	f, prs := newPRFixture(t)
	f.stageLabel(t, "env", "prod")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatal(err)
	}
	prs.onRead = func(pr *forge.PR, reads int) {
		if pr.Number == 1 && reads == 2 {
			pr.State = "closed"
		}
	}
	f.stageLabel(t, "tier", "gold")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "#1") {
		t.Fatalf("want ErrConflict naming #1, got %v", err)
	}
	if _, ok := f.proposal(t); ok {
		t.Fatal("a closed PR's record must go")
	}
	if n := f.draftCount(t); n != 1 {
		t.Fatalf("the draft must stay, got %d entries", n)
	}
	prs.onRead = nil
	out, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{})
	if err != nil || out.PRNumber != 1 || !out.Existing || prs.pr(1).State != "open" {
		t.Fatalf("want #1 reopened, got %+v %v", out, err)
	}
	if strings.Contains(f.show(t, prs.head+":web.yaml"), "env: prod") {
		t.Fatal("a reopened PR carries the draft alone")
	}
}

// An open PR dotvirt holds no record for cannot be rebuilt: refused, nothing
// pushed.
func TestProposeRefusesAnUnrecordedOpenPR(t *testing.T) {
	f, prs := newPRFixture(t)
	f.stageLabel(t, "env", "prod")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := f.c.store.DropProposal(f.id.Username, f.proj.Name); err != nil {
		t.Fatal(err)
	}
	before := revParse(t, f.proj.Repo, prs.head)
	f.stageLabel(t, "tier", "gold")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "#1") {
		t.Fatalf("want ErrConflict naming #1, got %v", err)
	}
	if revParse(t, f.proj.Repo, prs.head) != before {
		t.Fatal("nothing may be pushed")
	}
}

// Someone else pushes to the PR's branch: the lease on dotvirt's head refuses
// the rebuild, nothing is overwritten and the draft stays.
func TestProposeRefusesABranchMovedOutsideDotvirt(t *testing.T) {
	f, prs := newPRFixture(t)
	f.stageLabel(t, "env", "prod")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	gitRun(t, work, "clone", "-q", "-b", prs.head, f.proj.Repo, ".")
	writeWorkFile(t, work, "extra.yaml", []byte("kind: ConfigMap\n"))
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-qm", "theirs")
	gitRun(t, work, "push", "-q", "origin", prs.head)
	theirs := revParse(t, f.proj.Repo, prs.head)

	f.stageLabel(t, "tier", "gold")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "#1") {
		t.Fatalf("want ErrConflict naming #1, got %v", err)
	}
	if revParse(t, f.proj.Repo, prs.head) != theirs {
		t.Fatal("their commit must stand")
	}
	if n := f.draftCount(t); n != 1 {
		t.Fatalf("the draft must stay, got %d entries", n)
	}
}

// A whole-file edit of a VM the open PR changes field by field would undo
// the PR's change: refused, naming the PR and the way forward.
func TestProposeRefusesAWholeFileEditOverThePRsFieldEdit(t *testing.T) {
	f, prs := newPRFixture(t)
	initial := revParse(t, f.proj.Repo, "main")
	f.commitMain(t, "web.yaml", "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: web\n  namespace: alpha\nspec:\n  runStrategy: Halted\n")
	f.stageLabel(t, "env", "prod")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatal(err)
	}
	before := revParse(t, f.proj.Repo, prs.head)
	if _, err := f.c.RestoreVersion(f.id, f.proj, "", "alpha", "web", initial); err != nil {
		t.Fatalf("RestoreVersion: %v", err)
	}
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "#1") {
		t.Fatalf("want ErrConflict naming #1, got %v", err)
	}
	if revParse(t, f.proj.Repo, prs.head) != before {
		t.Fatal("nothing may be pushed")
	}
}

// Two tabs propose at once: the second waits for the first, then builds on
// its PR, so neither change is lost.
func TestConcurrentProposesKeepBothChanges(t *testing.T) {
	const db = "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: db\n  namespace: alpha\n"
	prs := newForgePRs()
	var f *proposeFixture
	var once sync.Once
	second := make(chan error, 1)
	entered := make(chan struct{}, 1)
	route := func(m, path string) (int, string, bool) {
		if m == "GET" && strings.Contains(path, "/pulls/main/") {
			once.Do(func() {
				// The other tab stages its change and proposes while the
				// first is asking the forge.
				go func() {
					if err := f.c.store.Stage("alice", "p", draft.Entry{Kind: draft.KindDelete, Namespace: "alpha", Name: "db", SourceFile: "db.yaml", BaseBlob: git.BlobID([]byte(db))}); err != nil {
						second <- err
						return
					}
					entered <- struct{}{}
					_, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{Title: "B"})
					second <- err
				}()
				<-entered
				// Without serialization the second propose would run its
				// own lookup here; give it the chance.
				time.Sleep(100 * time.Millisecond)
			})
			return http.StatusNotFound, "", true
		}
		return prs.route(m, path)
	}
	f = newProposeFixture(t, route)
	prs.f = f
	f.commitMain(t, "db.yaml", db)
	f.stageLabel(t, "env", "prod")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{Title: "A"}); err != nil {
		t.Fatalf("A: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("B: %v", err)
	}
	ls, err := exec.Command("git", "-C", f.proj.Repo, "ls-tree", "--name-only", prs.head).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ls), "db.yaml") || !strings.Contains(f.show(t, prs.head+":web.yaml"), "env: prod") {
		t.Fatalf("the branch must carry both changes; files %q", ls)
	}
	if n := len(prs.prs); n != 1 {
		t.Fatalf("one PR for one branch, got %d", n)
	}
}

// A create pins its path absent: a file landing there on main since is
// refused rather than overwritten.
func TestProposeRefusesACreateOverAFileMainGained(t *testing.T) {
	bare, live := seedBareWithLive(t)
	c := newTestCoordinator(t, true)
	c.live = live
	id, proj := auth.Identity{Username: "alice"}, project.ProjectInfo{Name: "p", Repo: bare}
	if _, err := c.Adopt(id, proj, "alpha", "copy"); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if rec, _ := c.store.List(id.Username, proj.Name); len(rec) != 1 || rec[0].BaseBlob != git.AbsentBlob {
		t.Fatalf("a create must pin its path absent, got %+v", rec)
	}
	work := t.TempDir()
	gitRun(t, work, "clone", "-q", bare, ".")
	writeWorkFile(t, work, "alpha/copy.yaml", []byte(liveVMYAML("copy", "")))
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-qm", "theirs")
	gitRun(t, work, "push", "-q", "origin", "main")
	if _, err := c.Propose(id, proj, model.ProposeRequest{}); !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "created on main") {
		t.Fatalf("want ErrConflict saying it was created on main, got %v", err)
	}
}

// A PR list read can skip an open PR that moved between pages. When the
// search finds none but the branch exists, it is asked again before a fresh
// build would force-push over the branch: here the second read finds the
// open PR, which has no record, so the propose is refused and the branch
// stands.
func TestProposeAsksAgainBeforeOverwritingAnExistingBranch(t *testing.T) {
	head := proposedBranchFor("alice", "p")
	var lists int
	var mu sync.Mutex
	f := newProposeFixture(t, func(m, path string) (int, string, bool) {
		if m != "GET" || !strings.HasSuffix(path, "/pulls") {
			return 0, "", false
		}
		mu.Lock()
		defer mu.Unlock()
		lists++
		if lists <= 2 { // the first search: its open and closed scans
			return http.StatusOK, "[]", true
		}
		return http.StatusOK, `[{"number":1,"state":"open","html_url":"http://forge/pulls/1","base":{"ref":"main"},"head":{"ref":"` + head + `"}}]`, true
	})
	work := t.TempDir()
	gitRun(t, work, "clone", "-q", f.proj.Repo, ".")
	gitRun(t, work, "checkout", "-qb", head)
	writeWorkFile(t, work, "theirs.yaml", []byte("kind: ConfigMap\n"))
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-qm", "the open PR's commit")
	gitRun(t, work, "push", "-q", "origin", head)
	before := revParse(t, f.proj.Repo, head)

	f.stageLabel(t, "env", "prod")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "#1") {
		t.Fatalf("want ErrConflict naming #1, got %v", err)
	}
	if revParse(t, f.proj.Repo, head) != before {
		t.Fatal("the branch must stand")
	}
}

// A record and no forge to ask about its PR: the branch is rebuilt with what
// the record carries under its lease, as for an open PR, never forced over.
func TestProposeWithoutAForgeRebuildsUnderTheRecord(t *testing.T) {
	c := newTestCoordinator(t, true)
	id, proj := auth.Identity{Username: "alice"}, project.ProjectInfo{Name: "p", Repo: seedBare(t)}
	branch := c.proposedBranch(id.Username, proj.Name)
	_, write, err := c.write(proj)
	if err != nil {
		t.Fatal(err)
	}
	env := draft.Entry{Kind: draft.KindEdit, Namespace: "alpha", Name: "web", SourceFile: "web.yaml", Edit: &model.VMEdit{SetLabels: map[string]string{"env": "prod"}}}
	first, err := write.CommitChangeset("main", branch, "env", c.toChangesetItems(proj, []draft.Entry{env}), git.Author{})
	if err != nil {
		t.Fatal(err)
	}
	rec := draft.Proposal{Repo: forge.NormalizeRepoURL(proj.Repo), Branch: branch, PR: 3, Head: "0123456789012345678901234567890123456789", Entries: []draft.Entry{env}}
	if err := c.store.SetProposal(id.Username, proj.Name, rec); err != nil {
		t.Fatal(err)
	}
	req := model.EditRequest{SourceFile: "web.yaml", VMEdit: model.VMEdit{SetLabels: map[string]string{"tier": "gold"}}}
	if _, err := c.StageEdit(id, proj, "alpha", "web", req); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Propose(id, proj, model.ProposeRequest{}); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("a record whose head the branch does not hold: want ErrConflict, got %v", err)
	}
	if revParse(t, proj.Repo, branch) != first.Hash {
		t.Fatal("nothing may be pushed")
	}
	rec.Head = first.Hash
	if err := c.store.SetProposal(id.Username, proj.Name, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Propose(id, proj, model.ProposeRequest{}); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	file, _ := exec.Command("git", "-C", proj.Repo, "show", branch+":web.yaml").Output()
	if !strings.Contains(string(file), "env: prod") || !strings.Contains(string(file), "tier: gold") {
		t.Fatalf("want both labels on the rebuilt branch:\n%s", file)
	}
	if got, ok, _ := c.store.Proposal(id.Username, proj.Name); !ok || got.PR != 3 || got.Head != revParse(t, proj.Repo, branch) {
		t.Fatalf("the record must follow the push, got %+v", got)
	}
}

// A failed re-read after the push leaves the PR's fate unknown: the push is
// recorded, the draft stays staged, and the next propose replays it, adding
// nothing twice.
func TestProposeKeepsTheDraftWhenThePRCannotBeReread(t *testing.T) {
	prs := newForgePRs()
	var mu sync.Mutex
	gets, fail := 0, false
	down := func(m, path string) (int, string, bool) {
		if m == "GET" && strings.HasSuffix(path, "/pulls/1") {
			mu.Lock()
			defer mu.Unlock()
			gets++
			if fail && gets == 2 {
				return http.StatusBadGateway, "down", true
			}
		}
		return 0, "", false
	}
	f := newProposeFixture(t, down, prs.route)
	prs.f = f
	f.stageLabel(t, "env", "prod")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gets, fail = 0, true
	mu.Unlock()
	f.stageLabel(t, "tier", "gold")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if n := f.draftCount(t); n != 1 {
		t.Fatalf("the draft must stay staged, got %d entries", n)
	}
	if rec, _ := f.proposal(t); rec.Head != revParse(t, f.proj.Repo, prs.head) || rec.Entries[0].Edit.SetLabels["tier"] != "gold" {
		t.Fatalf("the push must be recorded, got %+v", rec)
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if n := f.draftCount(t); n != 0 {
		t.Fatalf("the replay must unstage the draft, got %d entries", n)
	}
	if file := f.show(t, prs.head+":web.yaml"); strings.Count(file, "tier: gold") != 1 || !strings.Contains(file, "env: prod") {
		t.Fatalf("want both labels once:\n%s", file)
	}
}

// A push that landed while the record write after it failed: the record
// written ahead of the push still knows it, so the next propose goes on
// from the pushed head instead of reading it as someone else's push.
func TestProposeRecoversAPushTheRecordMissed(t *testing.T) {
	f, prs := newPRFixture(t)
	f.stageLabel(t, "env", "prod")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatal(err)
	}
	before, _ := f.proposal(t)
	f.stageLabel(t, "tier", "gold")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatal(err)
	}
	after, _ := f.proposal(t)
	// What the record says when the write after the push did not land.
	missed := before
	missed.Pending = &draft.Pending{Head: after.Head, Entries: after.Entries}
	if err := f.c.store.SetProposal(f.id.Username, f.proj.Name, missed); err != nil {
		t.Fatal(err)
	}
	f.stageLabel(t, "zone", "a")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	file := f.show(t, prs.head+":web.yaml")
	for _, want := range []string{"env: prod", "tier: gold", "zone: a"} {
		if !strings.Contains(file, want) {
			t.Fatalf("lost %q:\n%s", want, file)
		}
	}
}

// A whole-file edit made on base's version would replace the open PR's own
// whole-file edit of that object unseen: refused, naming the PR.
func TestProposeRefusesAWholeFileEditOverThePRsWholeFileEdit(t *testing.T) {
	f, prs := newPRFixture(t)
	base := f.show(t, "main:web.yaml")
	label := func(k string) string {
		return strings.Replace(base, "namespace: alpha\n", "namespace: alpha\n  labels:\n    "+k+": \"1\"\n", 1)
	}
	if _, err := f.c.StageUpdateManifest(f.id, f.proj, "", "alpha", "web", model.UpdateManifestRequest{YAML: label("a")}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatal(err)
	}
	before := revParse(t, f.proj.Repo, prs.head)
	if _, err := f.c.StageUpdateManifest(f.id, f.proj, "", "alpha", "web", model.UpdateManifestRequest{YAML: label("b")}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "#1") {
		t.Fatalf("want ErrConflict naming #1, got %v", err)
	}
	if revParse(t, f.proj.Repo, prs.head) != before {
		t.Fatal("nothing may be pushed")
	}
}

// A delete is pinned to the file it was staged against: once main adds to
// that file, the rebuild refuses rather than delete what it never saw.
func TestProposeRefusesADeleteOfAFileMainChanged(t *testing.T) {
	f, prs := newPRFixture(t)
	if _, err := f.c.StageDelete(f.id, f.proj, "", "alpha", "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatal(err)
	}
	f.commitMain(t, "web.yaml", f.show(t, "main:web.yaml")+"---\napiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: web2\n  namespace: alpha\n")
	f.commitMain(t, "db.yaml", "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: db\n  namespace: alpha\n")
	before := revParse(t, f.proj.Repo, prs.head)
	if _, err := f.c.StageDelete(f.id, f.proj, "", "alpha", "db"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "alpha/web") {
		t.Fatalf("want ErrConflict naming alpha/web, got %v", err)
	}
	if revParse(t, f.proj.Repo, prs.head) != before {
		t.Fatal("nothing may be pushed")
	}
}

// The forge would not open the PR: the push is recorded, and a PR the user
// then opens by hand on that push is adopted by the next propose.
func TestProposeAdoptsAPROpenedByHand(t *testing.T) {
	prs := newForgePRs()
	refuse := true
	var mu sync.Mutex
	create := func(m, path string) (int, string, bool) {
		mu.Lock()
		defer mu.Unlock()
		if refuse && m == "POST" && strings.HasSuffix(path, "/pulls") {
			return http.StatusInternalServerError, "boom", true
		}
		return 0, "", false
	}
	f := newProposeFixture(t, create, prs.route)
	prs.f = f
	f.stageLabel(t, "env", "prod")
	out, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{})
	if err != nil || out.CompareURL == "" {
		t.Fatalf("want the compare URL, got %+v %v", out, err)
	}
	head := revParse(t, f.proj.Repo, prs.head)
	if rec, ok := f.proposal(t); !ok || rec.PR != 0 || rec.Head != head {
		t.Fatalf("the push must be recorded without a PR, got %+v", rec)
	}
	mu.Lock()
	refuse = false
	mu.Unlock()
	byHand := forge.PR{Number: 1, State: "open", HTMLURL: "http://forge/pulls/1"}
	byHand.Head.Ref, byHand.Head.Sha, byHand.Base.Ref = prs.head, head, "main"
	prs.mu.Lock()
	prs.prs = append(prs.prs, byHand)
	prs.mu.Unlock()
	f.stageLabel(t, "tier", "gold")
	out, err = f.c.Propose(f.id, f.proj, model.ProposeRequest{})
	if err != nil || out.PRNumber != 1 || !out.Existing {
		t.Fatalf("want #1 adopted, got %+v %v", out, err)
	}
	if file := f.show(t, prs.head+":web.yaml"); !strings.Contains(file, "env: prod") || !strings.Contains(file, "tier: gold") {
		t.Fatalf("want both labels:\n%s", file)
	}
}

// A PR reopened for a push that then fails is closed again: it must not sit
// open on the content its closing turned down.
func TestPublishRecloseAfterAFailedPush(t *testing.T) {
	prs := newForgePRs()
	var f *proposeFixture
	broken := ""
	breakRemote := func(m, path string) (int, string, bool) {
		if m == "PATCH" && strings.HasSuffix(path, "/pulls/1") && strings.Contains(f.lastBody(), `"state":"open"`) {
			broken = f.proj.Repo + ".gone"
			if err := os.Rename(f.proj.Repo, broken); err != nil {
				t.Errorf("break the remote: %v", err)
			}
		}
		return 0, "", false
	}
	f = newProposeFixture(t, breakRemote, prs.route)
	prs.f = f
	t.Cleanup(func() {
		if broken != "" {
			_ = os.Rename(broken, f.proj.Repo)
		}
	})
	closed := forge.PR{Number: 1, State: "closed", HTMLURL: "http://forge/pulls/1"}
	closed.Head.Ref, closed.Base.Ref = prs.head, "main"
	prs.prs = append(prs.prs, closed)
	_, write, err := f.c.write(f.proj)
	if err != nil {
		t.Fatal(err)
	}
	items := []git.ChangesetItem{{Path: "new.yaml", NewContent: []byte("kind: ConfigMap\n")}}
	if _, _, _, err := f.c.publish(f.c.forgeFor(f.proj.Repo), write, prs.head, "m", "t", "b", items, git.Author{}, closed, true, nil); err == nil {
		t.Fatal("want the push to fail")
	}
	if got := prs.pr(1).State; got != "closed" {
		t.Fatalf("the PR must be closed again, is %s", got)
	}
}

// A record for another repo or branch is no record: the open PR is refused
// rather than rebuilt from entries meant elsewhere.
func TestProposeIgnoresARecordForAnotherBranch(t *testing.T) {
	f, _ := newPRFixture(t)
	f.stageLabel(t, "env", "prod")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatal(err)
	}
	rec, _ := f.proposal(t)
	rec.Branch = "dotvirt/proposed/someone/else"
	if err := f.c.store.SetProposal(f.id.Username, f.proj.Name, rec); err != nil {
		t.Fatal(err)
	}
	f.stageLabel(t, "tier", "gold")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "before dotvirt kept a record") {
		t.Fatalf("want the unrecorded-PR refusal, got %v", err)
	}
}

// The lease fails because the PR merged and its branch moved on: that is no
// one else's push, so the draft goes out on its own instead of advising to
// merge or close a PR that is already merged.
func TestProposeAfterAMergeMovedTheBranch(t *testing.T) {
	f, prs := newPRFixture(t)
	f.stageLabel(t, "env", "prod")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	gitRun(t, work, "clone", "-q", "-b", prs.head, f.proj.Repo, ".")
	writeWorkFile(t, work, "extra.yaml", []byte("kind: ConfigMap\n"))
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-qm", "a fixup before the merge")
	gitRun(t, work, "push", "-q", "origin", prs.head)
	prs.onRead = func(pr *forge.PR, reads int) {
		if pr.Number == 1 && reads == 2 {
			pr.State, pr.Merged = "closed", true
		}
	}
	f.stageLabel(t, "tier", "gold")
	out, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{})
	if err != nil || out.PRNumber != 2 {
		t.Fatalf("want the draft as new PR #2, got %+v %v", out, err)
	}
}

// No forge to ask, and the record's PR evidently merged: main now holds the
// whole-file content the PR carried, so its pin trips. The record goes, the
// draft is pushed on its own, and the result says so - instead of refusing
// every propose from then on.
func TestProposeWithoutAForgeDropsASupersededRecord(t *testing.T) {
	f, prs := newPRFixture(t)
	f.commitMain(t, "db.yaml", "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: db\n  namespace: alpha\nspec:\n  runStrategy: Always\n")
	a := strings.Replace(f.show(t, "main:web.yaml"), "namespace: alpha\n", "namespace: alpha\n  labels:\n    a: \"1\"\n", 1)
	if _, err := f.c.StageUpdateManifest(f.id, f.proj, "", "alpha", "web", model.UpdateManifestRequest{YAML: a}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatal(err)
	}
	f.commitMain(t, "web.yaml", a) // #1 merged
	f.c.forge = nil
	if _, err := f.c.StageEdit(f.id, f.proj, "alpha", "db", model.EditRequest{SourceFile: "db.yaml", VMEdit: model.VMEdit{SetLabels: map[string]string{"k": "v"}}}); err != nil {
		t.Fatal(err)
	}
	out, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{})
	if err != nil || !strings.Contains(out.Warning, "#1") {
		t.Fatalf("want the draft pushed with a note about #1, got %+v %v", out, err)
	}
	if _, ok := f.proposal(t); ok {
		t.Fatal("the superseded record must go")
	}
	if !strings.Contains(f.show(t, prs.head+":db.yaml"), "k: v") {
		t.Fatal("the draft must be pushed")
	}
}

// A delete whose file left main while main still declares the object at
// another path is a move, not a removal: refused naming the new path, the
// draft kept.
func TestProposeRefusesADeleteOfAMovedObject(t *testing.T) {
	f, _ := newPRFixture(t)
	if _, err := f.c.StageDelete(f.id, f.proj, "", "alpha", "web"); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	gitRun(t, work, "clone", "-q", f.proj.Repo, ".")
	gitRun(t, work, "mv", "web.yaml", "alpha-web.yaml")
	gitRun(t, work, "commit", "-qm", "move web")
	gitRun(t, work, "push", "-q", "origin", "main")
	f.commitMain(t, "README.md", "sync\n")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "alpha-web.yaml") {
		t.Fatalf("want ErrConflict naming alpha-web.yaml, got %v", err)
	}
	if n := f.draftCount(t); n != 1 {
		t.Fatalf("the draft must stay, got %d entries", n)
	}
}

// A refused lease on a branch no PR names says so plainly, never "#0".
func TestBranchMovedWithoutAPRNamesTheBranch(t *testing.T) {
	r := &proposeRun{c: &Coordinator{}, branch: "dotvirt/proposed/alice/p-x"}
	if err := r.commitFailed(git.ErrBranchMoved, nil, 0); !errors.Is(err, model.ErrConflict) || strings.Contains(err.Error(), "#0") || !strings.Contains(err.Error(), r.branch) {
		t.Fatalf("got %v", err)
	}
}

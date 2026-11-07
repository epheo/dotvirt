package changeset

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

// The PR merges while its branch is rebuilt. What it carried is in main now;
// the rebuilt branch gets a new PR for the draft alone.
func TestProposeAfterTheOpenPRMergedMeanwhile(t *testing.T) {
	f, prs := newPRFixture(t)
	f.stageLabel(t, "env", "prod")
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); err != nil {
		t.Fatal(err)
	}
	prs.onRead = func(pr *forge.PR, reads int) {
		if pr.Number == 1 && reads == 2 {
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
					if err := f.c.store.Stage("alice", "p", draft.Entry{Kind: draft.KindDelete, Namespace: "alpha", Name: "db", SourceFile: "db.yaml"}); err != nil {
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
	f.commitMain(t, "db.yaml", "apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: db\n  namespace: alpha\n")
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

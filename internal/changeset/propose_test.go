package changeset

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/forge"
)

// proposeFixture wires a Coordinator over a seeded bare repo and a forge Factory
// pointed at an httptest server scripted by routes. The bare repo path doubles as
// proj.Repo: forge.For parses its last two segments as owner/repo, while the API
// calls hit srv regardless of host - so routes match on the path suffix.
type proposeFixture struct {
	c       *Coordinator
	repos   *git.RepoSet
	proj    project.ProjectInfo
	id      auth.Identity
	forgeAt string

	mu   sync.Mutex
	sent []sentRequest // every forge call, in order
}

type sentRequest struct{ method, path, body string }

// sentTo is the bodies of every method call to a path ending in suffix, in order.
func (f *proposeFixture) sentTo(method, suffix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.sent {
		if r.method == method && strings.HasSuffix(r.path, suffix) {
			out = append(out, r.body)
		}
	}
	return out
}

// target is a project whose repo lives on the fixture's own forge, which is what the
// coordinator requires before it will trust a probe of that repo.
func (f *proposeFixture) target(name string) project.ProjectInfo {
	return project.ProjectInfo{
		Name:       name,
		Repo:       f.forgeAt + "/dotvirt/" + name + ".git",
		Namespaces: []string{name},
	}
}

// route returns the response for a (method, path-suffix) pair, or 0 to fall through.
type route func(method, path string) (status int, body string, matched bool)

func newProposeFixture(t *testing.T, routes ...route) *proposeFixture {
	t.Helper()
	f := &proposeFixture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.sent = append(f.sent, sentRequest{r.Method, r.URL.Path, string(body)})
		f.mu.Unlock()
		// Every scripted list is one page long.
		if page, _ := strconv.Atoi(r.URL.Query().Get("page")); page > 1 {
			_, _ = w.Write([]byte("[]"))
			return
		}
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
	repos := git.NewRepoSet(ctx, "", nil, true, nil, time.Hour)
	ff := forge.NewFactory(srv.URL, forge.StaticToken("tok"), false, "")
	f.c = New(store, repos, ff, nil, nil, nil, "main", "dotvirt/proposed")
	f.repos = repos
	f.proj = project.ProjectInfo{Name: "p", Repo: seedBare(t)}
	f.id = auth.Identity{Username: "alice"}
	f.forgeAt = srv.URL
	return f
}

// An existing repo must not block New Project: refusing would burn the name for
// a retried run (the repo is created before the draft). But reuse must SAY so;
// only the warning tells a former install's leftover from a harmless retry.
func TestCreateProjectReusesExistingRepoWithWarning(t *testing.T) {
	f := newProposeFixture(t, when("GET", "team-a", http.StatusOK, "{}"))
	view, err := f.c.StageCreateProject(f.id, f.proj, json.RawMessage(`{"name":"team-a","namespace":"team-a"}`), nil)
	if err != nil {
		t.Fatalf("a pre-existing repo must be reused, not refused: %v", err)
	}
	if !strings.Contains(view.Warning, "existing repo") {
		t.Fatalf("reuse must warn about the existing repo's contents, got %q", view.Warning)
	}
}

// A freshly created repo carries no leftover contents, so no reuse warning.
func TestCreateProjectFreshRepoNoWarning(t *testing.T) {
	f := newProposeFixture(t,
		when("GET", "team-a", http.StatusNotFound, ""),
		func(m, path string) (int, string, bool) {
			if m == "POST" && strings.HasPrefix(path, "/api/v1/orgs/") && strings.HasSuffix(path, "/repos") {
				return http.StatusCreated, "{}", true
			}
			return 0, "", false
		})
	view, err := f.c.StageCreateProject(f.id, f.proj, json.RawMessage(`{"name":"team-a","namespace":"team-a"}`), nil)
	if err != nil {
		t.Fatalf("StageCreateProject: %v", err)
	}
	if view.Warning != "" {
		t.Fatalf("fresh repo must not warn, got %q", view.Warning)
	}
}

// A repo annotation that still resolves means the project is already managed.
func TestAdoptProjectRefusesResolvingRepo(t *testing.T) {
	f := newProposeFixture(t, when("GET", "team-a", http.StatusOK, "{}"))
	_, err := f.c.AdoptProject(f.id, f.proj, f.target("team-a"), nil, nil)
	if !errors.Is(err, model.ErrConflict) {
		t.Fatalf("want model.ErrConflict when the repo still resolves, got %v", err)
	}
}

// An annotation the forge no longer resolves is the reported dead end: refusing it left
// the project unreachable through every path. The probe 404s, so adoption proceeds and
// re-creates the repo.
func TestAdoptProjectRecreatesLostRepo(t *testing.T) {
	f := newProposeFixture(t,
		when("GET", "team-a", http.StatusNotFound, ""),
		func(m, path string) (int, string, bool) {
			// The fixture's owner comes from the seeded bare-repo path, so match the shape.
			if m == "POST" && strings.HasPrefix(path, "/api/v1/orgs/") && strings.HasSuffix(path, "/repos") {
				return http.StatusCreated, "{}", true
			}
			return 0, "", false
		})
	if _, err := f.c.AdoptProject(f.id, f.proj, f.target("team-a"), nil, nil); err != nil {
		t.Fatalf("a lost repo must be re-created, not refused: %v", err)
	}
}

// A lost repo's clone failure, cached by inventory builds before adoption,
// must not outlive dotvirt re-creating it: the seed and the first propose
// open it right away.
func TestAdoptProjectRetriesRecreatedRepoAtOnce(t *testing.T) {
	var repoURL string
	f := newProposeFixture(t,
		when("GET", "team-a", http.StatusNotFound, ""),
		func(m, path string) (int, string, bool) {
			if m == "POST" && strings.HasPrefix(path, "/api/v1/orgs/") && strings.HasSuffix(path, "/repos") {
				gitRun(t, filepath.Dir(repoURL), "clone", "-q", "--bare", seedBare(t), repoURL)
				return http.StatusCreated, "{}", true
			}
			return 0, "", false
		})
	repoURL = filepath.Join(filepath.Dir(f.proj.Repo), "team-a.git")
	if _, _, err := f.repos.Get(repoURL); err == nil {
		t.Fatal("the lost repo must fail to open before adoption")
	}

	if _, err := f.c.AdoptProject(f.id, f.proj, f.target("team-a"), nil, nil); err != nil {
		t.Fatalf("AdoptProject: %v", err)
	}
	if _, _, err := f.repos.Get(repoURL); err != nil {
		t.Fatalf("re-created repo still served the cached failure: %v", err)
	}
}

// For keeps only the owner/repo, so a project on ANOTHER forge is probed by path here;
// when the probe misses, the real repo lives elsewhere and must not be re-homed to a
// fresh empty one.
func TestAdoptProjectRefusesRepoOnAnotherForge(t *testing.T) {
	f := newProposeFixture(t, when("GET", "team-a", http.StatusNotFound, ""))
	target := project.ProjectInfo{Name: "team-a", Namespaces: []string{"team-a"},
		Repo: "https://github.example/acme/team-a.git"}
	_, err := f.c.AdoptProject(f.id, f.proj, target, nil, nil)
	if !errors.Is(err, model.ErrConflict) {
		t.Fatalf("want model.ErrConflict for a repo this forge does not serve, got %v", err)
	}
}

// Foreign host, same owner/repo on this forge: the stranded aftermath of a host
// change. Re-home stages the host-free manifests; the PR is the migration.
func TestAdoptProjectRehomesForeignHostRepo(t *testing.T) {
	f := newProposeFixture(t, when("GET", "team-a", http.StatusOK, "{}"))
	target := project.ProjectInfo{Name: "team-a", Namespaces: []string{"team-a"},
		Repo: "https://old-forge.example/acme/team-a.git"}
	if _, err := f.c.AdoptProject(f.id, f.proj, target, nil, nil); err != nil {
		t.Fatalf("a same-owner repo on this forge must re-home, not refuse: %v", err)
	}
	entries, err := f.c.store.List(f.id.Username, f.proj.Name)
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want the namespace manifest staged, got %d entries", len(entries))
	}
	m := entries[0].Manifest
	if !strings.Contains(m, "acme/team-a.git") || strings.Contains(m, "old-forge.example") {
		t.Fatalf("staged ref must be host-free (owner/repo.git), got:\n%s", m)
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
	req := model.EditRequest{SourceFile: "web.yaml", VMEdit: model.VMEdit{SetLabels: map[string]string{"env": "prod"}}}
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
		when("GET", "/pulls", http.StatusOK, "[]"),
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
		// GET /pulls?... -> the branch's closed PR. Head ref must match the
		// per-(user,project) branch the coordinator builds.
		whenList("GET", "/pulls", http.StatusOK, "closed", false),
		when("PATCH", "/pulls/4", http.StatusOK, `{"number":4,"state":"open","html_url":"http://forge/pulls/4"}`),
	)
	out := f.stageAndPropose(t)
	if out.PRURL != "http://forge/pulls/4" || !out.Existing {
		t.Fatalf("got %+v, want reopened existing PR #4", out)
	}
	// The branch was rebuilt from the draft alone, so the reopened PR must say
	// so rather than keep the text of what was turned down.
	if p := f.sentTo("PATCH", "/pulls/4"); len(p) != 1 || !strings.Contains(p[0], `"title":"edit web"`) || !strings.Contains(p[0], `"state":"open"`) {
		t.Fatalf("want one reopen carrying the new title, got %q", p)
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
			`,"html_url":"http://forge/pulls/4","base":{"ref":"main"},"head":{"ref":"` + head + `"}}]`
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
	c := &Reader{proposed: "dotvirt/proposed"}
	return c.proposedBranch(user, proj)
}

// A draft whose whole intent is already true in git (here: a delete staged
// against a stale mirror, with git losing the file before the propose) must
// SELF-HEAL: propose answers a classified error naming what happened, clears
// the draft, and opens no PR - not the masked internal error that wedged the
// user until they guessed to unstage the item.
func TestProposeSelfHealsSatisfiedDraft(t *testing.T) {
	f := newProposeFixture(t, when("GET", "/pulls", http.StatusOK, "[]")) // any other forge call fails the test

	if _, err := f.c.StageDelete(f.id, f.proj, "", "alpha", "web"); err != nil {
		t.Fatalf("StageDelete: %v", err)
	}
	// The deletion lands in git out-of-band before the propose.
	work := t.TempDir()
	gitRun(t, work, "clone", "-q", f.proj.Repo, ".")
	gitRun(t, work, "rm", "-q", "web.yaml")
	gitRun(t, work, "commit", "-qm", "external delete")
	gitRun(t, work, "push", "-q", "origin", "main")

	_, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{Title: "drop web"})
	if !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("want classified ErrInvalid, got %v", err)
	}
	if !strings.Contains(err.Error(), "already matches git") {
		t.Fatalf("error must say what happened, got %q", err)
	}
	if n := f.draftCount(t); n != 0 {
		t.Fatalf("draft must self-heal to empty, still has %d entries", n)
	}
}

// openOrRecoverPR is the one way a pushed branch gets its PR: an open one is
// reused, a closed-unmerged one reopened, and one is created only when the
// branch has none - or its last PR merged, when a create the forge refuses
// falls back to the compare URL.
func TestOpenOrRecoverPR(t *testing.T) {
	head := proposedBranchFor("alice", "p")
	cases := []struct {
		name         string
		routes       []route
		wantNumber   int
		wantExisting bool
		wantOK       bool
	}{
		{"open PR is reused", []route{
			whenList("GET", "/pulls", http.StatusOK, "open", false),
		}, 4, true, true},
		{"closed PR is reopened", []route{
			whenList("GET", "/pulls", http.StatusOK, "closed", false),
			when("PATCH", "/pulls/4", http.StatusOK, `{"number":4,"state":"open","html_url":"http://forge/pulls/4"}`),
		}, 4, true, true},
		{"no PR is created", []route{
			when("GET", "/pulls", http.StatusOK, "[]"),
			when("POST", "/pulls", http.StatusCreated, `{"number":1,"state":"open","html_url":"http://forge/pulls/1"}`),
		}, 1, false, true},
		{"merged PR leaves the compare URL", []route{
			whenList("GET", "/pulls", http.StatusOK, "closed", true),
			when("POST", "/pulls", http.StatusConflict, "pull request already exists"),
		}, 0, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newProposeFixture(t, c.routes...)
			fc := f.c.forge.For(f.proj.Repo)
			var out model.ProposeResult
			ok := f.c.openOrRecoverPR(fc, &out, head, "t", "b")
			if ok != c.wantOK || out.PRNumber != c.wantNumber || out.Existing != c.wantExisting {
				t.Fatalf("got ok=%v %+v, want ok=%v #%d existing=%v", ok, out, c.wantOK, c.wantNumber, c.wantExisting)
			}
			if !ok && out.CompareURL == "" {
				t.Fatal("a missing PR must leave the compare URL")
			}
		})
	}
}

// Review state must track a PR whose head stands still: an approval lands and
// CI finishes without a push, and the next refresh shows both. Once the PR is
// settled (head, activity and a finished CI all unchanged) a refresh reads the
// list alone.
func TestOpenProposalsRefreshesUnchangedHead(t *testing.T) {
	head := proposedBranchFor("alice", "p")
	var reviews, status, updated string
	var reads int
	f := newProposeFixture(t,
		func(m, path string) (int, string, bool) {
			if m != "GET" {
				return 0, "", false
			}
			switch {
			case strings.HasSuffix(path, "/pulls"):
				return http.StatusOK, `[{"number":4,"state":"open","html_url":"http://forge/pulls/4","title":"edit web","updated_at":"` + updated +
					`","base":{"ref":"main"},"head":{"ref":"` + head + `","sha":"aaa"}}]`, true
			case strings.HasSuffix(path, "/branch_protections"):
				return http.StatusOK, `[]`, true
			case strings.Contains(path, "/reviews"):
				reads++
				return http.StatusOK, reviews, true
			case strings.Contains(path, "/status"):
				reads++
				return http.StatusOK, status, true
			}
			return 0, "", false
		})

	refresh := func() model.Proposal {
		t.Helper()
		prs, err := f.c.OpenProposals(f.proj)
		if err != nil || len(prs) != 1 {
			t.Fatalf("OpenProposals: %v (%d rows)", err, len(prs))
		}
		return prs[0]
	}

	reviews, status, updated = `[]`, `{"state":"pending","total_count":1}`, "2026-01-01T00:00:00Z"
	if p := refresh(); p.Approvals != 0 || p.Checks != "pending" {
		t.Fatalf("first refresh = approvals %d checks %q", p.Approvals, p.Checks)
	}
	reviews, status, updated = `[{"state":"APPROVED","user":{"login":"bob"}}]`, `{"state":"success","total_count":1}`, "2026-01-01T00:01:00Z"
	if p := refresh(); p.Approvals != 1 || p.Checks != "success" {
		t.Fatalf("an approval and a finished CI on the same head must show: approvals %d checks %q", p.Approvals, p.Checks)
	}
	before := reads
	if p := refresh(); p.Approvals != 1 || p.Checks != "success" {
		t.Fatalf("settled state must persist: approvals %d checks %q", p.Approvals, p.Checks)
	}
	if reads != before {
		t.Fatalf("a settled PR must not be re-read, got %d extra forge calls", reads-before)
	}
}

// An adopted namespace keeps what it already carries: Argo applies an omitted
// label as its removal, and OVN refuses removing the primary-network label.
func TestCreateProjectKeepsExistingNamespaceMetadata(t *testing.T) {
	f := newProposeFixture(t,
		when("GET", "tenant-a", http.StatusNotFound, ""),
		func(m, path string) (int, string, bool) {
			if m == "POST" && strings.HasPrefix(path, "/api/v1/orgs/") && strings.HasSuffix(path, "/repos") {
				return http.StatusCreated, "{}", true
			}
			return 0, "", false
		})
	live := func(string) (*netgen.LiveNamespace, error) {
		return &netgen.LiveNamespace{Labels: map[string]string{"tenant": "a", "k8s.ovn.org/primary-user-defined-network": ""}}, nil
	}
	view, err := f.c.StageCreateProject(f.id, f.proj, json.RawMessage(`{"name":"tenant-a","namespace":"tenant-a"}`), live)
	if err != nil {
		t.Fatalf("StageCreateProject: %v", err)
	}
	if len(view.Items) != 1 {
		t.Fatalf("want the namespace alone staged, got %d items", len(view.Items))
	}
	y := view.Items[0].YAML
	for _, want := range []string{"tenant: a", `k8s.ovn.org/primary-user-defined-network: ""`, "dotvirt.io/project: tenant-a"} {
		if !strings.Contains(y, want) {
			t.Errorf("staged namespace must carry %q:\n%s", want, y)
		}
	}
}

// A VM Network on a namespace that already exists is refused before the repo is
// created: OVN allows the primary-network label only at creation, and a refusal
// must not burn the name. The fixture fails on any forge call.
func TestCreateProjectRefusesVMNetworkOnExistingNamespace(t *testing.T) {
	f := newProposeFixture(t)
	live := func(string) (*netgen.LiveNamespace, error) { return &netgen.LiveNamespace{}, nil }
	_, err := f.c.StageCreateProject(f.id, f.proj,
		json.RawMessage(`{"name":"tenant-a","namespace":"tenant-a","vmNetwork":{"name":"vm-net","subnet":"10.40.0.0/24"}}`), live)
	if !errors.Is(err, model.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}

// The preview lists what staging would, restart flags included, and stages
// nothing; the staged edit then rides the view so an editor can extend it.
func TestPreviewEditStagesNothing(t *testing.T) {
	f := newProposeFixture(t)
	cpu := 64
	changes, err := f.c.PreviewEdit(f.proj, "alpha", "web", model.VMEdit{CPUCores: &cpu, SetLabels: map[string]string{"env": "prod"}})
	if err != nil {
		t.Fatalf("PreviewEdit: %v", err)
	}
	restart := map[string]bool{}
	for _, c := range changes {
		restart[c.Field] = c.Restart
	}
	if !restart["CPU"] || restart["Label env"] {
		t.Fatalf("want CPU to restart and the label not to, got %+v", changes)
	}
	if n := f.draftCount(t); n != 0 {
		t.Fatalf("preview staged %d entries", n)
	}
	if _, err := f.c.PreviewEdit(f.proj, "alpha", "ghost", model.VMEdit{CPUCores: &cpu}); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("a VM git does not declare: want ErrNotFound, got %v", err)
	}

	req := model.EditRequest{SourceFile: "web.yaml", VMEdit: model.VMEdit{CPUCores: &cpu}}
	if _, err := f.c.StageEdit(f.id, f.proj, "alpha", "web", req); err != nil {
		t.Fatalf("StageEdit: %v", err)
	}
	view, err := f.c.Get(f.id, f.proj)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(view.Items) != 1 || view.Items[0].Edit == nil || *view.Items[0].Edit.CPUCores != 64 {
		t.Fatalf("the staged edit must ride the view, got %+v", view.Items)
	}
}

// Staging goes on while a propose pushes: what lands meanwhile, new or a
// restage of a committed entry, is not in the PR and must stay staged.
func TestProposeKeepsWhatWasStagedDuringThePush(t *testing.T) {
	var during func()
	f := newProposeFixture(t,
		when("GET", "/pulls", http.StatusOK, "[]"),
		func(m, path string) (int, string, bool) {
			if m != "POST" || !strings.HasSuffix(path, "/pulls") {
				return 0, "", false
			}
			during()
			return http.StatusCreated, `{"number":1,"state":"open","html_url":"http://forge/pulls/1"}`, true
		})
	cpu := 8
	during = func() {
		if _, err := f.c.StageEdit(f.id, f.proj, "alpha", "web", model.EditRequest{SourceFile: "web.yaml", VMEdit: model.VMEdit{CPUCores: &cpu}}); err != nil {
			t.Errorf("StageEdit during the push: %v", err)
		}
		if err := f.c.store.Stage(f.id.Username, f.proj.Name, draft.Entry{Kind: draft.KindDelete, Namespace: "alpha", Name: "db", SourceFile: "db.yaml"}); err != nil {
			t.Errorf("stage during the push: %v", err)
		}
	}
	f.stageAndPropose(t)
	entries, err := f.c.store.List(f.id.Username, f.proj.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "db" || entries[1].Edit == nil || entries[1].Edit.CPUCores == nil {
		t.Fatalf("want the restaged web and the new db kept, got %+v", entries)
	}
}

// Re-proposing while the branch's PR is open adds to that PR: the new commit
// sits on the PR's head, nothing the PR carried is lost, and its title and
// body are rewritten to describe both changes under the user's message.
func TestReproposeAddsOntoTheOpenPR(t *testing.T) {
	head := proposedBranchFor("alice", "p")
	var opened atomic.Bool
	f := newProposeFixture(t,
		func(m, path string) (int, string, bool) {
			if m != "GET" || !strings.HasSuffix(path, "/pulls") {
				return 0, "", false
			}
			if !opened.Load() {
				return http.StatusOK, "[]", true
			}
			return http.StatusOK, `[{"number":1,"state":"open","html_url":"http://forge/pulls/1","body":"Why: launch.\n\n## Changes\n- old","base":{"ref":"main"},"head":{"ref":"` + head + `"}}]`, true
		},
		func(m, path string) (int, string, bool) {
			if m != "POST" || !strings.HasSuffix(path, "/pulls") {
				return 0, "", false
			}
			opened.Store(true)
			return http.StatusCreated, `{"number":1,"state":"open","html_url":"http://forge/pulls/1"}`, true
		},
		when("PATCH", "/pulls/1", http.StatusOK, `{"number":1,"state":"open"}`),
	)
	f.stageAndPropose(t)
	first := revParse(t, f.proj.Repo, head)

	req := model.EditRequest{SourceFile: "web.yaml", VMEdit: model.VMEdit{SetLabels: map[string]string{"tier": "gold"}}}
	view, err := f.c.StageEdit(f.id, f.proj, "alpha", "web", req)
	if err != nil {
		t.Fatalf("StageEdit: %v", err)
	}
	out, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{Title: view.DefaultTitle})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if out.PRNumber != 1 || !out.Existing {
		t.Fatalf("got %+v, want the open PR #1", out)
	}
	if parent := revParse(t, f.proj.Repo, head+"^"); parent != first {
		t.Fatalf("the new commit must sit on the PR's head %s, parent is %s", first, parent)
	}
	file, err := exec.Command("git", "-C", f.proj.Repo, "show", head+":web.yaml").Output()
	if err != nil || !strings.Contains(string(file), "env: prod") || !strings.Contains(string(file), "tier: gold") {
		t.Fatalf("the PR must carry both labels, web.yaml = %s (%v)", file, err)
	}
	patches := f.sentTo("PATCH", "/pulls/1")
	if len(patches) != 1 {
		t.Fatalf("want one PR rewrite, got %q", patches)
	}
	var edit forge.PREdit
	if err := json.Unmarshal([]byte(patches[0]), &edit); err != nil {
		t.Fatal(err)
	}
	if edit.Title == view.DefaultTitle || !strings.Contains(edit.Title, "web") {
		t.Errorf("the title must describe the whole PR, not this draft alone: %q", edit.Title)
	}
	for _, want := range []string{"Why: launch.", "prod", "gold"} {
		if !strings.Contains(edit.Body, want) {
			t.Errorf("the body must carry %q:\n%s", want, edit.Body)
		}
	}
	if strings.Contains(edit.Body, "- old") {
		t.Errorf("the generated part must be rebuilt, not kept:\n%s", edit.Body)
	}
	if n := f.draftCount(t); n != 0 {
		t.Fatalf("draft not cleared: %d entries", n)
	}
}

// A forge that cannot say whether the branch has an open PR stops the
// propose before the push: guessing "none" would rebuild an open PR's branch.
func TestProposeWithoutAForgeAnswerPushesNothing(t *testing.T) {
	f := newProposeFixture(t, when("GET", "/pulls", http.StatusBadGateway, "down"))
	req := model.EditRequest{SourceFile: "web.yaml", VMEdit: model.VMEdit{SetLabels: map[string]string{"env": "prod"}}}
	if _, err := f.c.StageEdit(f.id, f.proj, "alpha", "web", req); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{}); !errors.Is(err, model.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
	if out, _ := exec.Command("git", "-C", f.proj.Repo, "branch", "--list", "dotvirt/*").Output(); len(out) != 0 {
		t.Fatalf("nothing may be pushed, got %q", out)
	}
	if n := f.draftCount(t); n != 1 {
		t.Fatalf("the draft must stay, got %d entries", n)
	}
}

func revParse(t *testing.T, repo, rev string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "rev-parse", rev).Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", rev, err)
	}
	return strings.TrimSpace(string(out))
}

// A whole-file edit is made from base's version of the file. Extending an
// open PR that already changed that file would undo the PR's change: refused,
// naming the PR and the way forward, with nothing pushed.
func TestProposeRefusesWholeFileEditOverTheOpenPRsChange(t *testing.T) {
	head := proposedBranchFor("alice", "p")
	var opened atomic.Bool
	f := newProposeFixture(t,
		func(m, path string) (int, string, bool) {
			if m != "GET" || !strings.HasSuffix(path, "/pulls") {
				return 0, "", false
			}
			if !opened.Load() {
				return http.StatusOK, "[]", true
			}
			return http.StatusOK, `[{"number":1,"state":"open","html_url":"http://forge/pulls/1","base":{"ref":"main"},"head":{"ref":"` + head + `"}}]`, true
		},
		func(m, path string) (int, string, bool) {
			if m != "POST" || !strings.HasSuffix(path, "/pulls") {
				return 0, "", false
			}
			opened.Store(true)
			return http.StatusCreated, `{"number":1,"state":"open","html_url":"http://forge/pulls/1"}`, true
		},
	)
	// A second version on main, so there is an older one to restore.
	initial := revParse(t, f.proj.Repo, "main")
	work := t.TempDir()
	gitRun(t, work, "clone", "-q", f.proj.Repo, ".")
	writeWorkFile(t, work, "web.yaml", []byte("apiVersion: kubevirt.io/v1\nkind: VirtualMachine\nmetadata:\n  name: web\n  namespace: alpha\nspec:\n  runStrategy: Halted\n"))
	gitRun(t, work, "commit", "-qam", "halt web")
	gitRun(t, work, "push", "-q", "origin", "main")

	f.stageAndPropose(t) // PR #1 now labels web.yaml
	before := revParse(t, f.proj.Repo, head)
	if _, err := f.c.RestoreVersion(f.id, f.proj, "", "alpha", "web", initial); err != nil {
		t.Fatalf("RestoreVersion: %v", err)
	}
	_, err := f.c.Propose(f.id, f.proj, model.ProposeRequest{})
	if !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "#1") {
		t.Fatalf("want ErrConflict naming PR #1, got %v", err)
	}
	if after := revParse(t, f.proj.Repo, head); after != before {
		t.Fatalf("nothing may be pushed, the branch moved %s -> %s", before, after)
	}
}

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/tasks"
	"github.com/epheo/dotvirt/pkg/forge"
)

var (
	declaresNP  = schema.GroupKind{Group: model.KindNetpol.Group, Kind: model.KindNetpol.Kind}
	declaresDV  = schema.GroupKind{Group: model.KindDV.Group, Kind: model.KindDV.Kind}
	someCommit  = strings.Repeat("a", 40)
	historyPath = "/api/projects/team-a/history"
)

// gitReads answers the git reads, so a route refused is refused by its gate.
type gitReads struct{ *planeDraft }

func (gitReads) History(project.ProjectInfo, int) ([]model.Commit, error) { return nil, nil }
func (gitReads) NamespaceHistory(project.ProjectInfo, string, int) ([]model.Commit, error) {
	return nil, nil
}
func (gitReads) ObjectHistory(project.ProjectInfo, string, string, string, int) ([]model.Commit, error) {
	return nil, nil
}
func (gitReads) ObjectSpec(project.ProjectInfo, string, string, string) (model.ObjectSpec, error) {
	return model.ObjectSpec{}, nil
}
func (gitReads) Commit(project.ProjectInfo, string) (model.CommitDetail, error) {
	return model.CommitDetail{}, nil
}
func (gitReads) Proposal(project.ProjectInfo, int) (model.ProposalDetail, error) {
	return model.ProposalDetail{}, nil
}

// A repo holds every kind it declares, so a caller the apiserver would refuse
// one of them is refused the repo.
func TestRepoReadsNeedEveryDeclaredKind(t *testing.T) {
	fc := newFakeCluster(t)
	fc.readsVMs = tenantReadsTeamA
	reads := []string{historyPath, historyPath + "?namespace=team-a-dev", historyPath + "/" + someCommit}

	fc.serveKinds(model.KindNetpol, model.KindDV)
	d := &planeDraft{kinds: []schema.GroupKind{declaresNP}}
	s := fc.server(gitReads{d}, Config{BaseBranch: "main"})
	for _, path := range reads {
		if rec := do(s, tenantToken, http.MethodGet, path, ""); rec.Code == http.StatusForbidden {
			t.Errorf("%s: a caller listing every declared kind got 403: %s", path, rec.Body.String())
		}
	}

	d.kinds = append(d.kinds, declaresDV)
	for _, path := range append(reads, "/api/projects/team-a/proposals/1") {
		wantStatus(t, do(s, tenantToken, http.MethodGet, path, ""), http.StatusForbidden)
		if rec := do(s, adminToken, http.MethodGet, path, ""); rec.Code == http.StatusForbidden {
			t.Errorf("%s: admin got 403", path)
		}
	}
}

// One namespace of a project opens that namespace's history; the whole repo
// needs all of them, and a namespace outside the project is not there.
func TestRepoReadsFollowTheNamespacesSeen(t *testing.T) {
	fc := newFakeCluster(t)
	fc.namespaces = append(fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "team-a-prod", Labels: fc.namespaces[0].Labels, Annotations: fc.namespaces[0].Annotations,
	}})
	fc.readsVMs = tenantReadsTeamA
	fc.serveKinds(model.KindNetpol)
	s := fc.server(gitReads{&planeDraft{kinds: []schema.GroupKind{declaresNP}}}, Config{BaseBranch: "main"})

	if rec := do(s, tenantToken, http.MethodGet, historyPath+"?namespace=team-a-dev", ""); rec.Code == http.StatusForbidden {
		t.Errorf("own namespace: %s", rec.Body.String())
	}
	wantStatus(t, do(s, tenantToken, http.MethodGet, historyPath, ""), http.StatusForbidden)
	wantStatus(t, do(s, tenantToken, http.MethodGet, historyPath+"/"+someCommit, ""), http.StatusForbidden)
	wantStatus(t, do(s, tenantToken, http.MethodGet, historyPath+"?namespace=team-a-prod", ""), http.StatusNotFound)
	wantStatus(t, do(s, tenantToken, http.MethodGet, historyPath+"?namespace=orphans-ns", ""), http.StatusNotFound)
}

// A declared object's spec is that kind's to read, namespace visible or not.
func TestObjectReadsAskTheirKind(t *testing.T) {
	fc := newFakeCluster(t)
	fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		if token != tenantToken || a.Namespace != "team-a-dev" {
			return false
		}
		return a.Verb == "get" && a.Resource == "namespaces" || a.Verb == verbList && a.Resource != "networkpolicies"
	}
	s := fc.server(gitReads{&planeDraft{}}, Config{BaseBranch: "main"})
	for _, path := range []string{"/api/objects/networkpolicy/team-a-dev/web", "/api/objects/networkpolicy/team-a-dev/web/history"} {
		wantStatus(t, do(s, tenantToken, http.MethodGet, path, ""), http.StatusForbidden)
	}
	if rec := do(s, adminToken, http.MethodGet, "/api/objects/networkpolicy/team-a-dev/web", ""); rec.Code == http.StatusForbidden {
		t.Errorf("admin got 403: %s", rec.Body.String())
	}
}

// A project's rollup shows the rows the caller may list where they live, and
// the apply error, which can name any of them, only to who reads it all.
func TestProjectSyncViewKeepsWhatTheCallerLists(t *testing.T) {
	fc := newFakeCluster(t)
	fc.namespaces = append(fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "team-a-prod", Labels: fc.namespaces[0].Labels, Annotations: fc.namespaces[0].Annotations,
	}})
	fc.serveKinds(model.KindNetpol)
	srv := fc.server(&planeDraft{}, Config{BaseBranch: "main"})
	proj, ok := srv.projectByName("team-a")
	if !ok || len(proj.Namespaces) != 2 {
		t.Fatalf("project = %+v", proj)
	}
	asked := 0
	ps := model.ProjectSync{SyncError: "namespace team-a-prod: refused", Unhealthy: []model.ObjectHealth{
		{Group: model.KindNetpol.Group, Kind: model.KindNetpol.Kind, Namespace: "team-a-dev", Name: "web"},
		{Group: model.KindNetpol.Group, Kind: model.KindNetpol.Kind, Namespace: "team-a-prod", Name: "db"},
	}}
	view := func(allow func(*authzv1.ResourceAttributes) bool, p project.ProjectInfo, in model.ProjectSync) *model.ProjectSync {
		t.Helper()
		asked++
		visible := map[string]bool{}
		for _, ns := range p.Namespaces {
			visible[ns] = true
		}
		return srv.projectSyncView(context.Background(), viewAsking(srv, fmt.Sprint("tok-", asked), visible, allow), p, in)
	}

	listsInDev := func(a *authzv1.ResourceAttributes) bool {
		return a.Verb == verbList && a.Resource == model.KindNetpol.Plural && a.Namespace == "team-a-dev"
	}
	got := view(listsInDev, proj, ps)
	if len(got.Unhealthy) != 1 || got.Unhealthy[0].Name != "web" {
		t.Errorf("rows = %+v, want web alone", got.Unhealthy)
	}
	if got.SyncError != projectErrorForPartialViewers {
		t.Errorf("error = %q once a row dropped", got.SyncError)
	}

	got = view(func(*authzv1.ResourceAttributes) bool { return true }, proj, ps)
	if len(got.Unhealthy) != 2 || got.SyncError != ps.SyncError {
		t.Errorf("reader of everything got %+v", got)
	}

	// Nothing dropped, but team-a-prod is out of view: the error can still name it.
	partial := proj
	partial.Namespaces = []string{"team-a-dev"}
	quiet := model.ProjectSync{SyncError: ps.SyncError}
	if got = view(listsInDev, partial, quiet); got.SyncError != projectErrorForPartialViewers {
		t.Errorf("partial viewer read %q", got.SyncError)
	}
}

func teamALane(rows ...model.Proposal) []model.Proposal {
	for i := range rows {
		rows[i].Project = "team-a"
	}
	return rows
}

// A caller the repo refuses still opens their own open PR, and nothing else:
// another's, a revert of theirs, or one someone else pushed to.
func TestAProposalOpensForItsAuthorWhenTheRepoIsRefused(t *testing.T) {
	fc := newFakeCluster(t)
	fc.readsVMs = tenantReadsTeamA
	fc.serveKinds(model.KindNetpol, model.KindDV)
	d := &laneDraft{gatedDraft: &gatedDraft{planeDraft: &planeDraft{kinds: []schema.GroupKind{declaresNP, declaresDV}}}, foreign: 4}
	s := fc.server(d, Config{BaseBranch: "main"})
	s.proposals.Put("team-a", teamALane(
		model.Proposal{PRNumber: 1, Branch: "alice"},
		model.Proposal{PRNumber: 2, Branch: "bob"},
		model.Proposal{PRNumber: 3, Branch: "revert/alice"},
		model.Proposal{PRNumber: 4, Branch: "alice"},
	))
	id := auth.Identity{Token: tenantToken, Username: "alice"}
	open := func(n int) *httptest.ResponseRecorder {
		return doAs(s, id, http.MethodGet, fmt.Sprintf("/api/projects/team-a/proposals/%d", n))
	}
	rec := open(1)
	wantStatus(t, rec, http.StatusOK)
	if v := decodeBody[model.ProposalDetail](t, rec); !v.Proposal.Mine {
		t.Errorf("own proposal = %+v, want it marked theirs", v.Proposal)
	}
	for _, n := range []int{2, 3, 4, 99} {
		wantStatus(t, open(n), http.StatusForbidden)
	}
}

// What a repo shows of its PRs and merges narrows to the caller's own while
// they see part of the project. Seeing all of it keeps the rows, a missing
// kind or not: only opening one weighs the kinds.
func TestRowsNarrowForAPartialViewer(t *testing.T) {
	fc := newFakeCluster(t)
	second := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a-prod", Labels: fc.namespaces[0].Labels, Annotations: fc.namespaces[0].Annotations}}
	fc.namespaces = append(fc.namespaces, second)
	s := fc.server(gitReads{&planeDraft{}}, Config{BaseBranch: "main"})
	s.proposals.Put("team-a", teamALane(model.Proposal{PRNumber: 1, Branch: "alice"}, model.Proposal{PRNumber: 2, Branch: "bob"}))
	full, ok := s.projectByName("team-a")
	if !ok || len(full.Namespaces) != 2 || full.Repo == "" {
		t.Fatalf("project = %+v", full)
	}
	part := full
	part.Namespaces = []string{"team-a-dev"}
	alice := auth.Identity{Token: "tok", Username: "alice"}
	numbers := func(rows []model.Proposal) (out []int) {
		for _, r := range rows {
			out = append(out, r.PRNumber)
		}
		return out
	}
	if got := numbers(s.proposalsFor(alice, []project.ProjectInfo{part}, false)); !slices.Equal(got, []int{1}) {
		t.Errorf("partial viewer's rows = %v, want their own", got)
	}
	if got := numbers(s.proposalsFor(alice, []project.ProjectInfo{full}, false)); !slices.Equal(got, []int{1, 2}) {
		t.Errorf("a viewer of the whole project got %v", got)
	}

	repo := forge.NormalizeRepoURL(full.Repo)
	theirs, others := tasks.Merge{RepoURL: repo, Branch: "alice"}, tasks.Merge{RepoURL: repo, Branch: "bob"}
	sc := s.taskScope(alice, []project.ProjectInfo{part}, wideView(nil), false, false)
	if p, ok := sc.merge(theirs); !ok || p != "team-a" {
		t.Errorf("own merge = %q, %v", p, ok)
	}
	if _, ok := sc.merge(others); ok {
		t.Error("a partial viewer sees another's merge")
	}
	if _, ok := s.taskScope(alice, []project.ProjectInfo{full}, wideView(nil), false, false).merge(others); !ok {
		t.Error("a viewer of the whole project lost a merge")
	}
}

// A read refused says so, in the words the Changes pane shows.
func TestReadRefusalsSpeakOfReading(t *testing.T) {
	fc := newFakeCluster(t)
	fc.readsVMs = tenantReadsTeamA
	fc.serveKinds(model.KindNetpol, model.KindDV)
	s := fc.server(gitReads{&planeDraft{kinds: []schema.GroupKind{declaresDV}}}, Config{BaseBranch: "main"})
	rec := do(s, tenantToken, http.MethodGet, historyPath, "")
	wantStatus(t, rec, http.StatusForbidden)
	if body := rec.Body.String(); !strings.Contains(body, needForRead) || strings.Contains(body, needForChange) {
		t.Errorf("refusal = %q", body)
	}
}

// The shared project list costs one Resolve per namespace change and follows
// every one: a repo annotation and a project label included.
func TestProjectsResolveOncePerNamespaceChange(t *testing.T) {
	bus := eventbus.New()
	st, kube, _ := watchedState(t, bus, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        "a",
		Labels:      map[string]string{"dotvirt.io/project": "p"},
		Annotations: map[string]string{"dotvirt.io/repo": "https://x/one.git"},
	}})
	s := NewServer(Deps{State: st, Bus: bus, Reader: &fakeDraft{}, Draft: &fakeDraft{}, Resolver: project.NewResolver("dotvirt.io/project", "dotvirt.io/repo", "")})
	if first, second := s.AllProjects(), s.AllProjects(); len(first) != 1 || &first[0] != &second[0] {
		t.Errorf("a quiet cluster resolved twice: %+v", first)
	}
	update := func(change func(*corev1.Namespace)) {
		t.Helper()
		ns, err := kube.CoreV1().Namespaces().Get(context.Background(), "a", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		change(ns)
		if _, err := kube.CoreV1().Namespaces().Update(context.Background(), ns, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	update(func(ns *corev1.Namespace) { ns.Annotations["dotvirt.io/repo"] = "https://x/two.git" })
	waitUntil(t, "the new repo", func() bool {
		p, ok := s.projectByName("p")
		return ok && p.Repo == "https://x/two.git"
	})
	update(func(ns *corev1.Namespace) { ns.Labels["dotvirt.io/project"] = "q" })
	waitUntil(t, "the new project", func() bool {
		_, gone := s.projectByName("p")
		_, there := s.projectByName("q")
		return !gone && there
	})
}

// What dotvirt reads for a VM from git or its own account needs the VM read;
// what runs on the caller's token is the apiserver's to answer. A route that
// names neither is gated.
func TestVMRoutesAskOnlyWhereDotvirtReadsForTheCaller(t *testing.T) {
	var unnamed vmReads
	if unnamed != dotvirtReads {
		t.Fatal("the zero vmReads asks nothing")
	}
	fc := newFakeCluster(t)
	fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		return token == tenantToken && a.Namespace == "team-a-dev" && a.Verb == "get" && a.Resource == "namespaces"
	}
	s := fc.server(gitReads{&planeDraft{}}, Config{BaseBranch: "main"})
	const gated = "vm team-a-dev/web"
	rec := do(s, tenantToken, http.MethodGet, "/api/vms/team-a-dev/web/history", "")
	wantStatus(t, rec, http.StatusNotFound)
	if !strings.Contains(rec.Body.String(), gated) {
		t.Errorf("history = %q, want the VM read's refusal", rec.Body.String())
	}
	if rec := do(s, tenantToken, http.MethodGet, "/api/vms/team-a-dev/web/snapshots", ""); strings.Contains(rec.Body.String(), gated) {
		t.Errorf("snapshots stopped at the VM read: %q", rec.Body.String())
	}
	rec = do(s, tenantToken, http.MethodPost, "/api/networking/trace", traceFromWeb)
	wantStatus(t, rec, http.StatusNotFound)
	if !strings.Contains(rec.Body.String(), gated) {
		t.Errorf("trace = %q, want the VM read's refusal", rec.Body.String())
	}
}

const traceFromWeb = `{"source":{"namespace":"team-a-dev","vm":"web"},"destination":{"ip":"10.0.0.1"},"protocol":"TCP","port":443}`

// A trace places a VM by its address, which only its running instance holds.
func TestTraceNeedsTheInstances(t *testing.T) {
	fc := newFakeCluster(t)
	fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		if token != tenantToken || a.Namespace != "team-a-dev" {
			return false
		}
		return a.Verb == "get" && a.Resource == "namespaces" || a.Verb == verbList && a.Resource == "virtualmachines"
	}
	s := fc.server(gitReads{&planeDraft{}}, Config{BaseBranch: "main"})
	rec := do(s, tenantToken, http.MethodPost, "/api/networking/trace", traceFromWeb)
	wantStatus(t, rec, http.StatusNotFound)
	if !strings.Contains(rec.Body.String(), "vm team-a-dev/web") {
		t.Errorf("trace = %q, want the VM read's refusal", rec.Body.String())
	}
}

// The repo-read caps answer as the reads they stand for: a namespace's
// history for NamespaceCaps.RepoRead, the whole repo's for ProjectCaps.RepoRead.
func TestRepoReadCapsAnswerAsTheReads(t *testing.T) {
	teamAProd := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a-prod"}}
	for _, tc := range []struct {
		name           string
		kinds          []schema.GroupKind
		prod           bool
		token          string
		wantNS, wantPr bool
	}{
		{"lists every kind", []schema.GroupKind{declaresNP}, false, tenantToken, true, true},
		{"misses a kind", []schema.GroupKind{declaresNP, declaresDV}, false, tenantToken, false, false},
		{"sees part of the project", []schema.GroupKind{declaresNP}, true, tenantToken, true, false},
		{"admin", []schema.GroupKind{declaresNP, declaresDV}, true, adminToken, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeCluster(t)
			if tc.prod {
				prod := teamAProd
				prod.Labels, prod.Annotations = fc.namespaces[0].Labels, fc.namespaces[0].Annotations
				fc.namespaces = append(fc.namespaces, prod)
			}
			fc.readsVMs = tenantReadsTeamA
			fc.serveKinds(model.KindNetpol, model.KindDV)
			s := fc.server(gitReads{&planeDraft{kinds: tc.kinds}}, Config{BaseBranch: "main"})

			rec := do(s, tc.token, http.MethodGet, "/api/capabilities?namespace=team-a-dev", "")
			wantStatus(t, rec, http.StatusOK)
			c := decodeBody[model.Capabilities](t, rec).Namespace
			opens := func(path string) bool {
				return do(s, tc.token, http.MethodGet, path, "").Code != http.StatusForbidden
			}
			if c.RepoRead != tc.wantNS || opens(historyPath+"?namespace=team-a-dev") != tc.wantNS {
				t.Errorf("namespace: cap %v, want %v as its history route", c.RepoRead, tc.wantNS)
			}
			for _, path := range []string{historyPath, historyPath + "/" + someCommit} {
				if c.Project.RepoRead != tc.wantPr || opens(path) != tc.wantPr {
					t.Errorf("project: cap %v, want %v as %s", c.Project.RepoRead, tc.wantPr, path)
				}
			}
		})
	}
}

// A refusal says what the caller lacked, from the whole action: one that also
// changes something is a change, whatever question was denied.
func TestRefusalWordingFollowsTheAction(t *testing.T) {
	read := action{refQ(verbList, ssarVM, "a"), refQ(verbGet, ssarNamespace, "")}
	if err := refused("x", read); !strings.Contains(err.Error(), needForRead) {
		t.Errorf("a read refused as %q", err)
	}
	claim := action{refQ(verbGet, ssarNamespace, "a"), refQ(verbUpdate, ssarNamespace, "a")}
	if err := refused("get namespaces", claim); !strings.Contains(err.Error(), needForChange) {
		t.Errorf("a change refused on its get as %q", err)
	}
	if err := refused("x", read, claim); !strings.Contains(err.Error(), needForChange) {
		t.Errorf("any of a read and a change refused as %q", err)
	}
}

// A project's template library is its repo's content: listed and deployed
// from only by a caller who may read the repo. The shared library stays open.
func TestTemplateLibrariesAreRepoReads(t *testing.T) {
	fc := newFakeCluster(t)
	fc.readsVMs = tenantReadsTeamA
	// Authors VMs in team-a-dev but lists nothing past its view.
	fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		return token == adminToken || token == tenantToken && a.Namespace == "team-a-dev" && a.Verb != verbList
	}
	fc.serveKinds(model.KindNetpol, model.KindDV)
	d := &planeDraft{kinds: []schema.GroupKind{declaresNP}, templates: map[string][]model.Template{
		"platform": {{Name: "rhel9", Library: "platform"}},
		"team-a":   {{Name: "web", Library: "team-a"}},
	}}
	s := fc.server(d, Config{PlatformRepo: platformRepo})
	libraries := func(token string) string {
		rec := do(s, token, http.MethodGet, "/api/templates", "")
		wantStatus(t, rec, http.StatusOK)
		var out []string
		for _, tpl := range decodeBody[model.TemplateList](t, rec).Templates {
			out = append(out, tpl.Library+"/"+tpl.Name)
		}
		return strings.Join(out, ",")
	}
	deploy := func(library string) int {
		body := fmt.Sprintf(`{"namespace":"team-a-dev","template":"web","library":%q}`, library)
		return do(s, tenantToken, http.MethodPost, "/api/templates/deploy", body).Code
	}

	if got := libraries(tenantToken); got != "platform/rhel9,team-a/web" {
		t.Errorf("a caller reading the repo lists %s", got)
	}
	for _, lib := range []string{"", "team-a", "platform"} {
		if code := deploy(lib); code == http.StatusForbidden {
			t.Errorf("a caller reading the repo deploys from %q: 403", lib)
		}
	}

	d.kinds = append(d.kinds, declaresDV)
	if got := libraries(tenantToken); got != "platform/rhel9" {
		t.Errorf("a caller refused the repo lists %s", got)
	}
	for _, lib := range []string{"", "team-a"} {
		if code := deploy(lib); code != http.StatusForbidden {
			t.Errorf("a caller refused the repo deploys from %q: %d", lib, code)
		}
	}
	if code := deploy("platform"); code == http.StatusForbidden {
		t.Error("a caller refused its repo is refused the shared library")
	}
	if got := libraries(adminToken); got != "platform/rhel9,team-a/web" {
		t.Errorf("admin lists %s", got)
	}
}

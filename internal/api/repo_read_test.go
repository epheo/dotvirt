package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
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
		return token == tenantToken && a.Namespace == "team-a-dev" && a.Verb == verbList && a.Resource != "networkpolicies"
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
		id := auth.Identity{Token: fmt.Sprint("tok-", asked), Username: "u"}
		return srv.projectSyncView(context.Background(), id, newAsking(allow).c, p, in)
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

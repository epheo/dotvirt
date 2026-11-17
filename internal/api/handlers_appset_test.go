package api

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/epheo/dotvirt/internal/argo"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// The ApplicationSet plugin endpoint is pre-session (isOpenPath): its shared
// bearer is the ONLY thing between the internet-facing route and the full
// project->repo map, so the auth corner cases deserve pinning.
func TestAppSetPluginAuth(t *testing.T) {
	newServer := func(token string) *Server {
		bus := eventbus.New()
		sa := cluster.NewClient(fake.NewSimpleClientset(), nil, nil)
		return NewServer(Deps{
			State:    clusterstate.New(sa, "dotvirt.io/project", bus),
			Bus:      bus,
			Resolver: project.NewResolver("dotvirt.io/project", "dotvirt.io/repo", ""),
			Reader:   &fakeDraft{},
			Draft:    &fakeDraft{},
			Config:   Config{AppSetPluginToken: token},
		})
	}
	post := func(s *Server, authz string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/getparams.execute", nil)
		if authz != "" {
			r.Header.Set("Authorization", authz)
		}
		rec := httptest.NewRecorder()
		s.handleAppSetPlugin(rec, r)
		return rec
	}

	t.Run("unconfigured plugin is absent, not open", func(t *testing.T) {
		if rec := post(newServer(""), "Bearer anything"); rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("wrong or missing bearer is unauthorized", func(t *testing.T) {
		s := newServer("the-shared-token")
		for _, authz := range []string{"", "Bearer wrong", "Bearer the-shared-token-and-more", "the-shared-token"} {
			if rec := post(s, authz); rec.Code != http.StatusUnauthorized {
				t.Errorf("authz %q = %d, want 401", authz, rec.Code)
			}
		}
	})

	t.Run("correct bearer gets the generator envelope", func(t *testing.T) {
		rec := post(newServer("the-shared-token"), "Bearer the-shared-token")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d %q, want 200", rec.Code, rec.Body.String())
		}
		var out struct {
			Output struct {
				Parameters []map[string]any `json:"parameters"`
			} `json:"output"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		// Empty snapshot -> an empty parameter list, present, not null: the
		// ApplicationSet controller chokes on a missing field.
		if out.Output.Parameters == nil {
			t.Fatal("parameters must be an empty list, not absent")
		}
	})
}

// Each repo-backed project gets a destination its AppProject admits, and it
// holds still as namespaces join: the namespace named after the project, else
// the oldest, never platform space (the operator's reserved list included). A
// project no AppProject can confine gets no Application.
func TestAppSetPluginParams(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	label := func(name, proj string, age time.Duration) corev1.Namespace {
		return corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(t0.Add(age)),
			Labels: map[string]string{"dotvirt.io/project": proj}, Annotations: map[string]string{"dotvirt.io/repo": "https://forge/" + proj + ".git"}}}
	}
	base := []corev1.Namespace{
		label("team-b-prod", "team-b", 0), label("team-b-dev", "team-b", time.Hour),
		label("aaa", "team-c", 0), label("team-c", "team-c", time.Hour),
		label("kube-k", "team-k", -time.Hour), label("k-argo", "team-k", -time.Hour), label("k-work", "team-k", time.Hour),
		label("r-argo", "team-r", 0),
		label("bad", "Team_Bad", 0),
	}
	destinations := func(nss []corev1.Namespace) map[string]string {
		fc := newFakeCluster(t)
		fc.namespaces = append(fc.namespaces, nss...)
		s := fc.server(nil, Config{AppSetPluginToken: "tok"})
		body := `{"applicationSetName":"dotvirt-projects","input":{"parameters":{"reservedNamespaces":["k-argo","r-argo"]}}}`
		r := httptest.NewRequest(http.MethodPost, "/api/v1/getparams.execute", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		s.handleAppSetPlugin(rec, r)
		var out struct {
			Output struct {
				Parameters []map[string]any `json:"parameters"`
			} `json:"output"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		got := map[string]string{}
		for _, p := range out.Output.Parameters {
			got[p["project"].(string)] = p["namespace"].(string)
		}
		return got
	}
	want := map[string]string{"team-a": "team-a-dev", "team-b": "team-b-prod", "team-c": "team-c", "team-k": "k-work"}
	if got := destinations(base); !maps.Equal(got, want) {
		t.Fatalf("destinations = %v, want %v", got, want)
	}
	joined := append(slices.Clone(base), label("team-b-aaa", "team-b", 2*time.Hour), label("c-new", "team-c", 2*time.Hour))
	if got := destinations(joined); !maps.Equal(got, want) {
		t.Errorf("after namespaces joined: destinations = %v, want %v", got, want)
	}
}

// The ApplicationSet renders with missingkey=error: every project needs a path.
func TestAppSetPluginPath(t *testing.T) {
	fc := newFakeCluster(t)
	fc.namespaces = append(fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "in-folder",
		Labels:      map[string]string{"dotvirt.io/project": "folded"},
		Annotations: map[string]string{"dotvirt.io/repo": "https://forge/folded.git", "dotvirt.io/path": "clusters/a"}}})
	s := fc.server(nil, Config{AppSetPluginToken: "tok"})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/getparams.execute", strings.NewReader(`{"input":{"parameters":{"reservedNamespaces":[]}}}`))
	r.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	s.handleAppSetPlugin(rec, r)
	var out struct {
		Output struct {
			Parameters []map[string]any `json:"parameters"`
		} `json:"output"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range out.Output.Parameters {
		got[p["project"].(string)] = p["path"].(string)
	}
	if got["folded"] != "clusters/a" || got["team-a"] != "." {
		t.Errorf("paths = %v", got)
	}
}

// The generator lifts allowEmpty only while Argo holds an empty source that
// git itself reached by moving on from what Argo applied, read from the same
// source: a deliberate removal of a project's last objects. Every other hold
// keeps Argo's guard: a repo lost and re-created (another history), a mirror
// that cannot tell yet, git that did not move, a re-pointed source, git that
// still declares what renders to nothing. The platform's static app never
// holds. The inventory says the same of each.
func TestAppSetPluginLiftsAllowEmptyOnlyForADeliberateRemoval(t *testing.T) {
	removed := newRemovalRepo(t)
	removed.remove(t)
	same := newRemovalRepo(t)
	moved := newRemovalRepo(t)
	moved.remove(t)
	recreated := "file://" + seedPlatformRepo(t, map[string]string{"README.md": "recovered\n"})
	unopened := newRemovalRepo(t)
	unopened.remove(t)
	stale := newRemovalRepo(t)
	declared := newRemovalRepo(t)
	declared.edit(t)
	platform := "file://" + seedPlatformRepo(t, map[string]string{"README.md": "lost\n"})

	fc := newFakeCluster(t)
	var apps []map[string]any
	for _, p := range []struct {
		name, repo, compared, applied, appliedPath string
	}{
		{"team-del", removed.url, removed.head(t), removed.applied, "."},
		{"team-rec", recreated, gitHead(t, strings.TrimPrefix(recreated, "file://")), removed.applied, "."},
		{"team-new", unopened.url, unopened.head(t), unopened.applied, "."},
		{"team-same", same.url, same.applied, same.applied, "."},
		{"team-full", declared.url, declared.head(t), declared.applied, "."},
		{"team-moved", moved.url, moved.head(t), moved.applied, "elsewhere"},
		{"team-stale", stale.url, "", stale.applied, "."},
	} {
		fc.namespaces = append(fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: p.name,
			Labels: map[string]string{"dotvirt.io/project": p.name}, Annotations: map[string]string{"dotvirt.io/repo": p.repo}}})
		if p.name == "team-stale" {
			continue // its app is added once the mirror is open, below
		}
		apps = append(apps, heldApp(p.name, p.repo, p.compared, p.applied, p.appliedPath))
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repos := git.NewRepoSet(ctx, "", "", nil, false, nil, time.Hour)
	for _, repo := range []string{removed.url, same.url, moved.url, recreated, stale.url, declared.url} {
		if _, _, err := repos.Get(repo, ""); err != nil {
			t.Fatal(err)
		}
	}
	// Opened before the removal landed: the mirror has not fetched it.
	stale.remove(t)
	apps = append(apps, heldApp("team-stale", stale.url, stale.head(t), stale.applied, "."))
	platformApp := heldApp("platform", platform, gitHead(t, strings.TrimPrefix(platform, "file://")), removed.applied, ".")
	platformApp["metadata"] = map[string]any{"namespace": "argocd", "name": "dotvirt-platform"}
	apps = append(apps, platformApp)
	fc.on(http.MethodGet, "/apis/argoproj.io/v1alpha1/applications", unstructuredList("argoproj.io/v1alpha1", "Application", apps...))
	s := fc.server(nil, Config{AppSetPluginToken: "tok", BaseBranch: "main", PlatformRepo: platform})
	s.drift = syncedSnapshot(t, fc)
	s.repos = repos

	want := map[string]model.Hold{
		"team-del": model.HoldDeleting, "team-rec": model.HoldReplaced, "team-new": model.HoldPending,
		"team-same": model.HoldMismatch, "team-full": model.HoldMismatch, "team-moved": model.HoldMismatch,
		"team-stale": model.HoldPending, "team-a": "",
	}
	got := map[string]model.Hold{}
	for _, p := range s.AllProjects() {
		if _, ok := want[p.Name]; ok {
			got[p.Name] = s.EmptyHold(p)
		}
	}
	if !maps.Equal(got, want) {
		t.Errorf("holds = %v, want %v", got, want)
	}
	if h := s.EmptyHold(s.platformProject()); h != "" {
		t.Errorf("platform hold = %q: its static app is never lifted, so never held here", h)
	}
	lifted := generated(t, s, want)
	if want := map[string]any{"team-del": true, "team-rec": false, "team-new": false, "team-same": false,
		"team-full": false, "team-moved": false, "team-stale": false, "team-a": false}; !maps.Equal(lifted, want) {
		t.Errorf("allowEmpty = %v, want %v", lifted, want)
	}

	// The removal lands: Argo syncs and holds nothing. The next run closes it.
	fc2 := newFakeCluster(t)
	synced := heldApp("team-del", removed.url, removed.head(t), removed.head(t), ".")
	_ = unstructured.SetNestedField(synced, "Synced", "status", "sync", "status")
	_ = unstructured.SetNestedSlice(synced, []any{}, "status", "resources")
	fc2.on(http.MethodGet, "/apis/argoproj.io/v1alpha1/applications", unstructuredList("argoproj.io/v1alpha1", "Application", synced))
	_ = fc2.server(nil, Config{})
	s.drift = syncedSnapshot(t, fc2)
	if lifted := generated(t, s, want); lifted["team-del"] != false {
		t.Errorf("after the removal synced: allowEmpty = %v, want false", lifted["team-del"])
	}
}

// generated runs the generator once, as Argo calls it: each wanted project's
// allowEmpty.
func generated(t *testing.T, s *Server, want map[string]model.Hold) map[string]any {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/getparams.execute", strings.NewReader(`{"input":{"parameters":{"reservedNamespaces":[]}}}`))
	r.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	s.handleAppSetPlugin(rec, r)
	var out struct {
		Output struct {
			Parameters []map[string]any `json:"parameters"`
		} `json:"output"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	lifted := map[string]any{}
	for _, p := range out.Output.Parameters {
		if _, ok := want[p["project"].(string)]; ok {
			lifted[p["project"].(string)] = p["allowEmpty"]
		}
	}
	return lifted
}

// removalRepo is a repo whose main declares a VM, what Argo last applied;
// remove then deletes it, and edit changes it, each as a new commit on main.
type removalRepo struct {
	url, bare, work, applied string
}

func newRemovalRepo(t *testing.T) *removalRepo {
	t.Helper()
	bare := seedPlatformRepo(t, map[string]string{"team/web.yaml": "kind: VirtualMachine\nmetadata: {name: web, namespace: team}\n"})
	r := &removalRepo{url: "file://" + bare, bare: bare, work: filepath.Join(t.TempDir(), "work"), applied: gitHead(t, bare)}
	r.git(t, "clone", "-q", bare, r.work)
	return r
}

func (r *removalRepo) git(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (r *removalRepo) remove(t *testing.T) {
	r.git(t, "-C", r.work, "rm", "-q", "team/web.yaml")
	r.git(t, "-C", r.work, "commit", "-qm", "delete web")
	r.git(t, "-C", r.work, "push", "-q", "origin", "main")
}

func (r *removalRepo) edit(t *testing.T) {
	if err := os.WriteFile(filepath.Join(r.work, "team/web.yaml"), []byte("kind: VirtualMachine\nmetadata: {name: web, namespace: team, labels: {a: b}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(t, "-C", r.work, "commit", "-qam", "edit web")
	r.git(t, "-C", r.work, "push", "-q", "origin", "main")
}

func (r *removalRepo) head(t *testing.T) string { return gitHead(t, r.bare) }

func gitHead(t *testing.T, bare string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", bare, "rev-parse", "main").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// heldApp is a project's generated Application as Argo holds it at compared,
// after it applied applied from appliedPath of repo: every object it tracks
// would be pruned.
func heldApp(project, repo, compared, applied, appliedPath string) map[string]any {
	return map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"namespace": "argocd", "name": "dotvirt-" + project, "labels": map[string]any{"dotvirt.io/project": project}},
		"spec": map[string]any{
			"source":     map[string]any{"repoURL": repo, "path": ".", "targetRevision": "main"},
			"syncPolicy": map[string]any{"automated": map[string]any{"prune": true, "selfHeal": true}},
		},
		"status": map[string]any{
			"sync":      map[string]any{"status": "OutOfSync", "revision": compared},
			"history":   []any{map[string]any{"revision": applied, "source": map[string]any{"repoURL": repo, "path": appliedPath}}},
			"resources": []any{map[string]any{"kind": "VirtualMachine", "namespace": project, "name": "web", "requiresPruning": true}},
		},
	}
}

// syncedSnapshot is the Application snapshot over fc, once its LIST landed.
func syncedSnapshot(t *testing.T, fc *fakeCluster) *argo.Snapshot {
	t.Helper()
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	kc := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: t\n  cluster: {server: %s, insecure-skip-tls-verify: true}\ncontexts:\n- name: t\n  context: {cluster: t, user: t}\ncurrent-context: t\nusers:\n- name: t\n  user: {token: sa-token}\n", fc.srv.URL)
	if err := os.WriteFile(kubeconfig, []byte(kc), 0o600); err != nil {
		t.Fatal(err)
	}
	ac, err := argo.NewSAClient(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	snap := argo.NewSnapshot(ac, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	snap.Run(ctx)
	syncCtx, done := context.WithTimeout(ctx, 10*time.Second)
	defer done()
	if err := snap.WaitForSync(syncCtx); err != nil {
		t.Fatalf("Applications never synced: %v", err)
	}
	return snap
}

package argo

import (
	"context"
	"errors"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
)

// appWithSource builds an Application with a single-source repoURL + targetRevision
// and the given status.resources.
func appWithSource(ns, name, repoURL string, resources []any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"namespace": ns, "name": name},
		"spec":       map[string]any{"source": map[string]any{"repoURL": repoURL, "targetRevision": "main"}},
		"status":     map[string]any{"resources": resources},
	}}
}

func TestSnapshotDriftNilUntilSynced(t *testing.T) {
	s := NewSnapshot(&Client{}, nil)
	if s.Drift() != nil {
		t.Fatal("Drift() must be nil before the initial LIST has landed (else every VM flashes NotTracked)")
	}
	_ = s.store().Replace([]any{app("argocd", "managed", []any{vmResource("prod", "vm-a", "Synced", "Healthy")})}, "1")

	d := s.Drift()
	if d == nil {
		t.Fatal("Drift() must be non-nil once synced")
	}
	if d["prod/vm-a"].Sync != model.SyncSynced {
		t.Errorf("vm-a drift wrong: %+v", d["prod/vm-a"])
	}
}

func TestSnapshotmanagingApp(t *testing.T) {
	s := NewSnapshot(&Client{}, nil)
	_ = s.apps.Add(appWithSource("argocd", "team-a", "https://forge/team-a.git",
		[]any{vmResource("prod", "vm-a", "Synced", "Healthy")}))
	s.synced.Mark()

	ref, ok := s.managingApp("prod", "vm-a")
	if !ok || ref.Name != "team-a" || ref.Namespace != "argocd" || ref.TargetRevision != "main" {
		t.Fatalf("managingApp wrong: %+v ok=%v", ref, ok)
	}
	if _, ok := s.managingApp("prod", "ghost"); ok {
		t.Error("managingApp should miss an unmanaged VM")
	}
}

func TestSnapshotRefreshForRepoMatchesByCanonicalURL(t *testing.T) {
	a := appWithSource("argocd", "team-a", "https://Forge/team-a.git",
		[]any{vmResource("prod", "vm-a", "Synced", "Healthy")})
	other := appWithSource("argocd", "team-b", "https://forge/team-b.git", []any{})
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), a, other)

	s := NewSnapshot(&Client{dyn: dyn}, nil)
	_ = s.apps.Add(a)
	_ = s.apps.Add(other)
	s.synced.Mark()

	// Pushed as the html_url form (no .git, lowercase host) - must still match the
	// annotated clone_url form (.git, mixed case) via canonical normalization.
	s.RefreshForRepo(context.Background(), "https://forge/team-a")

	got, err := dyn.Resource(applicationsGVR).Namespace("argocd").Get(context.Background(), "team-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetAnnotations()["argocd.argoproj.io/refresh"] != "hard" {
		t.Errorf("team-a should be hard-refreshed, annotations=%v", got.GetAnnotations())
	}
	gotB, err := dyn.Resource(applicationsGVR).Namespace("argocd").Get(context.Background(), "team-b", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, has := gotB.GetAnnotations()["argocd.argoproj.io/refresh"]; has {
		t.Error("team-b (unrelated repo) must not be refreshed")
	}
}

func TestSnapshotRefreshForRepoNoopUntilSynced(t *testing.T) {
	a := appWithSource("argocd", "team-a", "https://forge/team-a.git", nil)
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), a)
	s := NewSnapshot(&Client{dyn: dyn}, nil)
	_ = s.apps.Add(a)
	// synced is false - RefreshForRepo must be a no-op (no patch).
	s.RefreshForRepo(context.Background(), "https://forge/team-a")

	got, _ := dyn.Resource(applicationsGVR).Namespace("argocd").Get(context.Background(), "team-a", metav1.GetOptions{})
	if _, has := got.GetAnnotations()["argocd.argoproj.io/refresh"]; has {
		t.Error("RefreshForRepo must not patch before the snapshot has synced")
	}
}

// Argo rewrites every Application on each reconcile; only a write that moves
// what the view holds may publish DriftChanged, or every frame rebuilds every
// few seconds to identical bytes.
func TestDriftPublishesOnlyOnContentChange(t *testing.T) {
	bus := eventbus.New()
	s := NewSnapshot(&Client{}, bus)
	st := s.store()
	a := app("argocd", "managed", []any{vmResource("prod", "vm-a", "Synced", "Healthy")})
	_ = st.Replace([]any{a}, "1")
	v := bus.Version(eventbus.DriftChanged)
	if v == 0 {
		t.Fatal("the initial LIST must publish")
	}
	view := s.Drift()

	tick := a.DeepCopy()
	_ = unstructured.SetNestedField(tick.Object, "2026-11-07T10:00:00Z", "status", "reconciledAt")
	_ = st.Update(tick)
	if bus.Version(eventbus.DriftChanged) != v {
		t.Fatal("a reconcile that moved no drift published DriftChanged")
	}
	if got := s.Drift(); reflect.ValueOf(got).Pointer() != reflect.ValueOf(view).Pointer() {
		t.Error("an unchanged view must stay the shared map, not a rebuilt copy")
	}

	_ = st.Update(app("argocd", "managed", []any{vmResource("prod", "vm-a", "OutOfSync", "Healthy")}))
	if bus.Version(eventbus.DriftChanged) == v {
		t.Fatal("a drift move did not publish")
	}
	if got := s.Drift()["prod/vm-a"].Sync; got != model.SyncOutOfSync {
		t.Errorf("vm-a sync = %q after the move", got)
	}
}

// ObjectDriftGen must advance only when a NON-VM object's drift content changes:
// Application churn with identical drift (reconciledAt ticks) and VM-only drift moves
// must not bump it - it feeds the catalog watermark, and a false bump would send
// otherwise-suppressed frames and refetch the catalog for nothing.
func TestObjectDriftGen(t *testing.T) {
	s := NewSnapshot(&Client{}, nil)
	st := s.store()
	a := app("argocd", "net", []any{
		vmResource("prod", "vm-a", "Synced", "Healthy"),
		udnResource("prod", "db-net", "Synced", ""),
	})
	_ = st.Replace([]any{a}, "1")

	gen := s.ObjectDriftGen()
	if gen == 0 {
		t.Fatal("the first view holding a segment must move the gen off zero")
	}

	// Store churn, same content.
	_ = st.Update(a.DeepCopy())
	if got := s.ObjectDriftGen(); got != gen {
		t.Errorf("gen bumped on contentless churn: %d -> %d", gen, got)
	}

	// A VM-only drift move: rides the frame itself, must not bump the catalog gen.
	_ = st.Update(app("argocd", "net", []any{
		vmResource("prod", "vm-a", "OutOfSync", "Degraded"),
		udnResource("prod", "db-net", "Synced", ""),
	}))
	if got := s.ObjectDriftGen(); got != gen {
		t.Errorf("gen bumped on a VM-only drift change: %d -> %d", gen, got)
	}

	// The segment's drift moves: gen must bump.
	_ = st.Update(app("argocd", "net", []any{
		vmResource("prod", "vm-a", "OutOfSync", "Degraded"),
		udnResource("prod", "db-net", "OutOfSync", "Progressing"),
	}))
	if got := s.ObjectDriftGen(); got != gen+1 {
		t.Errorf("gen did not bump on a segment drift change: %d -> %d", gen, got)
	}
}

// The own app is not a claim (it survives a lost repo while git declares
// nothing); every other app is, under each tracking-id identity.
func TestForeignAppsExcludesOwnRepo(t *testing.T) {
	s := NewSnapshot(nil, nil)
	if s.ForeignApps("https://forge.example/dotvirt/team-a.git") != nil {
		t.Fatal("must be nil before the initial LIST, so callers refuse rather than misread")
	}
	s.synced.Mark()
	for _, a := range []*unstructured.Unstructured{
		appWithSource("openshift-gitops", "dotvirt-team-a", "https://forge.example/dotvirt/team-a.git", nil),
		appWithSource("openshift-gitops", "dotvirt-platform", "https://forge.example/dotvirt/platform.git", nil),
	} {
		if err := s.apps.Add(a); err != nil {
			t.Fatal(err)
		}
	}
	// The repo annotation may differ from the app's source in case and .git suffix;
	// the join normalizes like every other repo-keyed reader.
	got := s.ForeignApps("https://forge.example/dotvirt/TEAM-A")
	if got["dotvirt-team-a"] || got["openshift-gitops_dotvirt-team-a"] {
		t.Errorf("own app must not be a claim: %v", got)
	}
	for _, id := range []string{"dotvirt-platform", "openshift-gitops_dotvirt-platform", "openshift-gitops/dotvirt-platform"} {
		if !got[id] {
			t.Errorf("foreign app missing identity %q: %v", id, got)
		}
	}
}

// PrunePending relays Argo's own requiresPruning, namespace-scoped and sorted
// for a stable warning.
func TestPrunePendingRelaysArgoComparison(t *testing.T) {
	s := NewSnapshot(nil, nil)
	if s.PrunePending("https://forge.example/dotvirt/team-a.git", []string{"team-a"}) != nil {
		t.Fatal("must be nil before the initial LIST")
	}
	s.synced.Mark()
	res := func(kind, ns, name string, prune bool) map[string]any {
		return map[string]any{"kind": kind, "namespace": ns, "name": name, "requiresPruning": prune}
	}
	for _, a := range []*unstructured.Unstructured{
		appWithSource("openshift-gitops", "dotvirt-team-a", "https://forge.example/dotvirt/team-a.git", []any{
			res("VirtualMachine", "team-a", "web", true),
			res("UserDefinedNetwork", "team-a", "backend", true),
			res("VirtualMachine", "team-a", "db", false),      // declared: not at risk
			res("VirtualMachine", "elsewhere", "other", true), // another project's namespace
		}),
		appWithSource("openshift-gitops", "dotvirt-b", "https://forge.example/dotvirt/team-b.git", []any{
			res("VirtualMachine", "team-a", "foreign", true), // another repo's app
		}),
	} {
		if err := s.apps.Add(a); err != nil {
			t.Fatal(err)
		}
	}
	got := s.PrunePending("https://forge.example/dotvirt/team-a.git", []string{"team-a"})
	want := []model.ObjectRef{
		{Kind: "UserDefinedNetwork", Namespace: "team-a", Name: "backend"},
		{Kind: "VirtualMachine", Namespace: "team-a", Name: "web"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestResyncRecordsCallerAndMissesAsNotFound(t *testing.T) {
	a := appWithSource("argocd", "team-a", "https://forge/team-a.git",
		[]any{vmResource("prod", "vm-a", "Synced", "Healthy")})
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), a)
	s := NewSnapshot(&Client{dyn: dyn}, nil)
	_ = s.apps.Add(a)
	s.synced.Mark()

	if _, err := s.Resync(context.Background(), "alice", "prod", "ghost"); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("unmanaged VM: err = %v, want ErrNotFound", err)
	}

	res, err := s.Resync(context.Background(), `al"ice`, "prod", "vm-a")
	if err != nil {
		t.Fatal(err)
	}
	if res.Application != "team-a" || res.Revision != "main" {
		t.Errorf("result = %+v", res)
	}
	got, err := dyn.Resource(applicationsGVR).Namespace("argocd").Get(context.Background(), "team-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	user, _, _ := unstructured.NestedString(got.Object, "operation", "initiatedBy", "username")
	if user != `al"ice` {
		t.Errorf("initiatedBy.username = %q, want the caller", user)
	}
}

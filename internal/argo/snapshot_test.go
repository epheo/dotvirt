package argo

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

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
	settle(s)

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
	_ = s.apps.Add(dotvirtApp("argocd", "team-a", "https://forge/team-a.git",
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

// settle runs the derive a pending store move asks for, as the deriver would.
func settle(s *Snapshot) {
	select {
	case <-s.dirty:
		s.derive()
	default:
	}
}

// Argo writes every Application on each reconcile; at 1000 apps a derive per
// write is most of a core even with no change. A write that moves only the
// reconcile stamps must not mark the view dirty; any other move must.
func TestReconcileOnlyWriteSkipsDerive(t *testing.T) {
	s := NewSnapshot(&Client{}, nil)
	st := s.store()
	a := benchApp(0)
	a.SetResourceVersion("1")
	a.SetGeneration(1)
	_ = st.Replace([]any{a}, "1")
	settle(s)
	pending := func() bool { return len(s.dirty) > 0 }

	tick := a.DeepCopy()
	tick.SetResourceVersion("2")
	tick.SetGeneration(2)
	tick.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "argocd-application-controller"}})
	_ = unstructured.SetNestedField(tick.Object, "2026-11-07T10:00:00Z", "status", "reconciledAt")
	_ = unstructured.SetNestedField(tick.Object, "2026-11-07T10:00:00Z", "status", "observedAt")
	_ = st.Update(tick)
	if pending() {
		t.Fatal("a reconcile-only write marked the view dirty")
	}

	moved := tick.DeepCopy()
	moved.SetResourceVersion("3")
	_ = unstructured.SetNestedField(moved.Object, "OutOfSync", "status", "sync", "status")
	_ = st.Update(moved)
	if !pending() {
		t.Fatal("a sync status move did not mark the view dirty")
	}
	settle(s)

	unlisted := moved.DeepCopy()
	_ = unstructured.SetNestedField(unlisted.Object, "x", "status", "summary", "externalURLs")
	_ = st.Update(unlisted)
	if !pending() {
		t.Fatal("a field outside the named noise must count as a change")
	}
}

// A derive scans every Application, and a sync wave or a relist writes many in a
// burst: moves that land while one derive runs must share the next derive, not
// queue one each.
func TestDeriveCoalescesBursts(t *testing.T) {
	bus := eventbus.New()
	s := NewSnapshot(&Client{}, bus)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.deriveLoop(ctx)
	st := s.store()
	var list []any
	for i := range 200 {
		list = append(list, benchApp(i))
	}
	_ = st.Replace(list, "1")
	if err := s.WaitForSync(ctx); err != nil {
		t.Fatal(err)
	}

	// Each write moves content (a distinct apply error), so every derive publishes.
	failing := func(n int) *unstructured.Unstructured {
		a := benchApp(0)
		status := a.Object["status"].(map[string]any)
		status["resources"].([]any)[0] = vmResource("ns-0", "vm-0", "OutOfSync", "Healthy")
		results := status["operationState"].(map[string]any)["syncResult"].(map[string]any)["resources"].([]any)
		results[0] = map[string]any{
			"group": "kubevirt.io", "kind": "VirtualMachine", "namespace": "ns-0", "name": "vm-0",
			"status": "SyncFailed", "message": fmt.Sprintf("boom-%d", n),
		}
		return a
	}
	const writes = 500
	burst := make([]*unstructured.Unstructured, writes)
	for i := range burst {
		burst[i] = failing(i)
	}
	before := bus.Version(eventbus.DriftChanged)
	for _, a := range burst {
		_ = st.Update(a)
	}
	last := fmt.Sprintf("boom-%d", writes-1)
	deadline := time.Now().Add(10 * time.Second)
	for s.Drift()["ns-0/vm-0"].Message != last {
		if time.Now().After(deadline) {
			t.Fatal("the last write never reached the view")
		}
		time.Sleep(time.Millisecond)
	}
	if derives := bus.Version(eventbus.DriftChanged) - before; derives > writes/10 {
		t.Fatalf("%d writes in a burst cost %d derives, want them coalesced", writes, derives)
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
	settle(s)
	v := bus.Version(eventbus.DriftChanged)
	if v == 0 {
		t.Fatal("the initial LIST must publish")
	}
	view := s.Drift()

	tick := a.DeepCopy()
	_ = unstructured.SetNestedField(tick.Object, "2026-11-07T10:00:00Z", "status", "reconciledAt")
	_ = st.Update(tick)
	settle(s)
	if bus.Version(eventbus.DriftChanged) != v {
		t.Fatal("a reconcile that moved no drift published DriftChanged")
	}
	if got := s.Drift(); reflect.ValueOf(got).Pointer() != reflect.ValueOf(view).Pointer() {
		t.Error("an unchanged view must stay the shared map, not a rebuilt copy")
	}

	_ = st.Update(app("argocd", "managed", []any{vmResource("prod", "vm-a", "OutOfSync", "Healthy")}))
	settle(s)
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
	settle(s)

	gen := s.ObjectDriftGen()
	if gen == 0 {
		t.Fatal("the first view holding a segment must move the gen off zero")
	}

	// Store churn, same content.
	_ = st.Update(a.DeepCopy())
	settle(s)
	if got := s.ObjectDriftGen(); got != gen {
		t.Errorf("gen bumped on contentless churn: %d -> %d", gen, got)
	}

	// A VM-only drift move: rides the frame itself, must not bump the catalog gen.
	_ = st.Update(app("argocd", "net", []any{
		vmResource("prod", "vm-a", "OutOfSync", "Degraded"),
		udnResource("prod", "db-net", "Synced", ""),
	}))
	settle(s)
	if got := s.ObjectDriftGen(); got != gen {
		t.Errorf("gen bumped on a VM-only drift change: %d -> %d", gen, got)
	}

	// The segment's drift moves: gen must bump.
	_ = st.Update(app("argocd", "net", []any{
		vmResource("prod", "vm-a", "OutOfSync", "Degraded"),
		udnResource("prod", "db-net", "OutOfSync", "Progressing"),
	}))
	settle(s)
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

// dotvirtApp is a project Application as the ApplicationSet generates it.
func dotvirtApp(ns, name, repoURL string, resources []any) *unstructured.Unstructured {
	a := appWithSource(ns, name, repoURL, resources)
	a.SetLabels(map[string]string{projectAppLabel: name})
	return a
}

func TestResyncRecordsCallerAndMissesAsNotFound(t *testing.T) {
	a := dotvirtApp("argocd", "team-a", "https://forge/team-a.git",
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
	// The caller's right covers the VM, so the sync names it and nothing else.
	res2, _, _ := unstructured.NestedSlice(got.Object, "operation", "sync", "resources")
	want := map[string]any{"group": "kubevirt.io", "kind": "VirtualMachine", "namespace": "prod", "name": "vm-a"}
	if len(res2) != 1 || !reflect.DeepEqual(res2[0], want) {
		t.Errorf("sync.resources = %v, want only %v", res2, want)
	}
}

// An Application dotvirt did not generate is never synced on a user's behalf,
// from the snapshot or from the live fallback.
func TestResyncSkipsForeignApplications(t *testing.T) {
	foreign := appWithSource("argocd", "cluster-config", "https://forge/admin.git",
		[]any{vmResource("prod", "vm-a", "Synced", "Healthy")})
	for _, synced := range []bool{true, false} {
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{applicationsGVR: "ApplicationList"}, foreign.DeepCopy())
		s := NewSnapshot(&Client{dyn: dyn}, nil)
		if synced {
			_ = s.apps.Add(foreign)
			s.synced.Mark()
		}
		if _, err := s.Resync(context.Background(), "alice", "prod", "vm-a"); !errors.Is(err, model.ErrNotFound) {
			t.Errorf("synced=%v: err = %v, want ErrNotFound", synced, err)
		}
		got, err := dyn.Resource(applicationsGVR).Namespace("argocd").Get(context.Background(), "cluster-config", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, patched := got.Object["operation"]; patched {
			t.Errorf("synced=%v: a foreign Application was patched", synced)
		}
	}
}

// TestResyncHidesTheSAStatus pins that the SA patch's own apiserver verdict
// never reaches the caller as theirs: a 401 there would end the caller's
// session, a 403 would read as the caller's RBAC.
func TestResyncHidesTheSAStatus(t *testing.T) {
	a := dotvirtApp("argocd", "team-a", "https://forge/team-a.git",
		[]any{vmResource("prod", "vm-a", "Synced", "Healthy")})
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), a)
	dyn.PrependReactor("patch", "applications", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewUnauthorized("sa token expired")
	})
	s := NewSnapshot(&Client{dyn: dyn}, nil)
	_ = s.apps.Add(a)
	s.synced.Mark()

	_, err := s.Resync(context.Background(), "alice", "prod", "vm-a")
	if apierrors.IsUnauthorized(err) || !errors.Is(err, model.ErrUnavailable) {
		t.Fatalf("err = %v; want ErrUnavailable with the SA status dropped", err)
	}
}

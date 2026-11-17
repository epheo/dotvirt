package argo

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
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

// ObjectDriftGen must advance only when a NON-VM object's drift content changes
// in that scope: Application churn with identical drift (reconciledAt ticks),
// VM-only drift moves and another namespace's objects must not bump it - it
// feeds each caller's catalog watermark, and a false bump would send
// otherwise-suppressed frames and refetch the catalog for nothing.
func TestObjectDriftGen(t *testing.T) {
	s := NewSnapshot(&Client{}, nil)
	st := s.store()
	cudn := map[string]any{"group": "k8s.ovn.org", "kind": "ClusterUserDefinedNetwork", "name": "shared", "status": "Synced"}
	nsObj := map[string]any{"group": "", "kind": "Namespace", "name": "dev", "status": "Synced"}
	resources := func(prodNet, devNet, vm string, extra ...any) []any {
		return append([]any{
			vmResource("prod", "vm-a", vm, "Healthy"),
			udnResource("prod", "db-net", prodNet, ""),
			udnResource("dev", "db-net", devNet, ""),
		}, extra...)
	}
	set := func(rs []any) {
		_ = st.Update(app("argocd", "net", rs))
		settle(s)
	}
	_ = st.Replace([]any{app("argocd", "net", resources("Synced", "Synced", "Synced"))}, "1")
	settle(s)

	prod, dev := s.ObjectDriftGen("prod"), s.ObjectDriftGen("dev")
	if prod == 0 || dev == 0 {
		t.Fatal("the first view holding a network must move its scope's gen off zero")
	}
	want := func(what string, wantProd, wantDev uint64) {
		t.Helper()
		if gp, gd := s.ObjectDriftGen("prod"), s.ObjectDriftGen("dev"); gp != wantProd || gd != wantDev {
			t.Errorf("%s: prod %d dev %d, want %d %d", what, gp, gd, wantProd, wantDev)
		}
	}

	set(resources("Synced", "Synced", "Synced"))
	want("contentless churn", prod, dev)

	set(resources("Synced", "Synced", "OutOfSync"))
	want("VM-only drift", prod, dev)

	set(resources("OutOfSync", "Synced", "OutOfSync"))
	want("a prod network moved", prod+1, dev)

	set(resources("OutOfSync", "OutOfSync", "OutOfSync"))
	want("a dev network moved", prod+1, dev+1)

	// A Namespace object is its own scope; another cluster-scoped kind scopes
	// by its kind alone.
	set(resources("OutOfSync", "OutOfSync", "OutOfSync", cudn, nsObj))
	want("cluster-scoped objects added", prod+1, dev+2)
	if s.ObjectDriftGen(ClusterScope("k8s.ovn.org", "ClusterUserDefinedNetwork")) == 0 {
		t.Error("a cluster-scoped object must move its kind's scope")
	}
	if s.ObjectDriftGen(ClusterScope("policy.networking.k8s.io", "AdminNetworkPolicy")) != 0 {
		t.Error("a cluster-scoped object moved another kind's scope")
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

// PrunePending relays Argo's own requiresPruning for every app on the repo,
// whatever folder it reads (a merge moves the revision each of them syncs):
// every namespace and cluster-scoped object, never another repo's, sorted for
// a stable warning. Before the initial LIST it cannot say.
func TestPrunePendingRelaysArgoComparison(t *testing.T) {
	const teamA = "https://forge.example/dotvirt/team-a.git"
	s := NewSnapshot(nil, nil)
	if refs, known := s.PrunePending(teamA); known || refs != nil {
		t.Fatalf("before the initial LIST: %v known=%v, want nothing known", refs, known)
	}
	s.synced.Mark()
	res := func(kind, ns, name string, prune bool) map[string]any {
		return map[string]any{"kind": kind, "namespace": ns, "name": name, "requiresPruning": prune}
	}
	folder := appWithSource("openshift-gitops", "dotvirt-team-c", teamA, []any{
		res("VirtualMachine", "team-c", "folder", true),
	})
	_ = unstructured.SetNestedField(folder.Object, "team-c", "spec", "source", "path")
	for _, a := range []*unstructured.Unstructured{
		appWithSource("openshift-gitops", "dotvirt-team-a", teamA, []any{
			res("VirtualMachine", "team-a", "web", true),
			res("UserDefinedNetwork", "team-a", "backend", true),
			res("VirtualMachine", "team-a", "db", false), // declared: not at risk
			res("VirtualMachine", "team-a-ops", "ops", true),
			res("Namespace", "", "team-a-old", true),
		}),
		folder,
		appWithSource("openshift-gitops", "dotvirt-b", "https://forge.example/dotvirt/team-b.git", []any{
			res("VirtualMachine", "team-a", "foreign", true), // another repo's app
		}),
	} {
		if err := s.apps.Add(a); err != nil {
			t.Fatal(err)
		}
	}
	want := []model.ObjectRef{
		{Kind: "Namespace", Name: "team-a-old"},
		{Kind: "UserDefinedNetwork", Namespace: "team-a", Name: "backend"},
		{Kind: "VirtualMachine", Namespace: "team-a", Name: "web"},
		{Kind: "VirtualMachine", Namespace: "team-a-ops", Name: "ops"},
		{Kind: "VirtualMachine", Namespace: "team-c", Name: "folder"},
	}
	if got, known := s.PrunePending(teamA); !known || !slices.Equal(got, want) {
		t.Fatalf("got %v known=%v, want %v", got, known, want)
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

// The SA may patch Applications only in the ArgoCD namespace: one elsewhere is
// refused with a reason, never patched, and never refreshed.
func TestApplicationWritesStayInTheArgoNamespace(t *testing.T) {
	in := dotvirtApp("argocd", "team-a", "https://forge/team-a.git",
		[]any{vmResource("prod", "vm-a", "Synced", "Healthy")})
	out := dotvirtApp("elsewhere", "team-b", "https://forge/team-a.git",
		[]any{vmResource("dev", "vm-b", "Synced", "Healthy")})
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), in, out)
	var patched []string
	dyn.PrependReactor("patch", "applications", func(a k8stesting.Action) (bool, runtime.Object, error) {
		patched = append(patched, a.GetNamespace()+"/"+a.(k8stesting.PatchAction).GetName())
		return false, nil, nil
	})
	s := NewSnapshot(&Client{dyn: dyn}, nil).WithWriteNamespace("argocd")
	_ = s.apps.Add(in)
	_ = s.apps.Add(out)
	s.synced.Mark()

	_, err := s.Resync(context.Background(), "alice", "dev", "vm-b")
	if !errors.Is(err, model.ErrUnavailable) || !strings.Contains(err.Error(), "only in the ArgoCD namespace argocd") {
		t.Errorf("Resync outside the ArgoCD namespace: err = %v, want ErrUnavailable naming it", err)
	}
	if _, err := s.Resync(context.Background(), "alice", "prod", "vm-a"); err != nil {
		t.Errorf("Resync in the ArgoCD namespace: %v", err)
	}
	s.RefreshForRepo(context.Background(), "https://forge/team-a")
	if want := []string{"argocd/team-a", "argocd/team-a"}; !slices.Equal(patched, want) {
		t.Errorf("patched %v, want %v", patched, want)
	}
}

// A hand-labelled copy outside the ArgoCD namespace never wins over the
// Application inside it, whatever order the store or the LIST returns.
func TestResyncPrefersTheArgoNamespace(t *testing.T) {
	vm := []any{vmResource("prod", "vm-a", "Synced", "Healthy")}
	in := dotvirtApp("argocd", "team-a", "https://forge/team-a.git", vm)
	// "apps" sorts before "argocd", and the fake LIST sorts by namespace: the
	// live fallback meets the copy first.
	out := dotvirtApp("apps", "team-a-copy", "https://forge/team-a.git", vm)
	s := NewSnapshot(&Client{}, nil).WithWriteNamespace("argocd")
	for _, apps := range [][]*unstructured.Unstructured{{in, out}, {out, in}} {
		if ref, ok := pickManaging(apps, "prod", "vm-a", s.writable); !ok || ref.Namespace != "argocd" {
			t.Errorf("order %s first: picked %+v, want the ArgoCD namespace's", apps[0].GetNamespace(), ref)
		}
	}
	if ref, ok := pickManaging([]*unstructured.Unstructured{out}, "prod", "vm-a", s.writable); !ok || ref.Namespace != "apps" {
		t.Errorf("only a copy elsewhere: picked %+v ok=%v, want it, for Resync to refuse with a reason", ref, ok)
	}
	for _, synced := range []bool{true, false} {
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{applicationsGVR: "ApplicationList"}, out.DeepCopy(), in.DeepCopy())
		s := NewSnapshot(&Client{dyn: dyn}, nil).WithWriteNamespace("argocd")
		if synced {
			_ = s.apps.Add(out)
			_ = s.apps.Add(in)
			s.synced.Mark()
		}
		res, err := s.Resync(context.Background(), "alice", "prod", "vm-a")
		if err != nil || res.Application != "team-a" {
			t.Errorf("synced=%v: Resync = %+v, %v; want team-a in the ArgoCD namespace", synced, res, err)
		}
	}
}

// installs pin the resync operation's exact shape in admission; any change to
// it must be deliberate.
func TestResyncPatchShapeIsPinned(t *testing.T) {
	got, err := resyncPatch(`al"ice`, "main", "prod", "vm-a")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"operation":{"initiatedBy":{"username":"al\"ice"},"sync":{"revision":"main","resources":[{"group":"kubevirt.io","kind":"VirtualMachine","namespace":"prod","name":"vm-a"}]},"info":[{"name":"reason","value":"dotvirt re-sync from git"}]}}`
	if string(got) != want {
		t.Errorf("resync patch =\n%s\nwant\n%s", got, want)
	}
}

// A resync merged into an operation already requested or running would
// rewrite it, so the caller is told to wait and nothing is patched; a finished
// operation does not block.
func TestResyncWaitsForTheRunningOperation(t *testing.T) {
	withOp := func(a *unstructured.Unstructured) {
		a.Object["operation"] = map[string]any{"sync": map[string]any{"revision": "main"}}
	}
	withPhase := func(phase string) func(*unstructured.Unstructured) {
		return func(a *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(a.Object, phase, "status", "operationState", "phase")
		}
	}
	cases := []struct {
		name  string
		shape func(*unstructured.Unstructured)
		busy  bool
	}{
		{"operation requested", withOp, true},
		{"operation running", withPhase("Running"), true},
		{"operation terminating", withPhase("Terminating"), true},
		{"last operation succeeded", withPhase("Succeeded"), false},
		{"last operation failed", withPhase("Failed"), false},
		{"last operation errored", withPhase("Error"), false},
	}
	for _, tc := range cases {
		for _, synced := range []bool{true, false} {
			a := dotvirtApp("argocd", "team-a", "https://forge/team-a.git",
				[]any{vmResource("prod", "vm-a", "Synced", "Healthy")})
			tc.shape(a)
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
				map[schema.GroupVersionResource]string{applicationsGVR: "ApplicationList"}, a.DeepCopy())
			patched := false
			dyn.PrependReactor("patch", "applications", func(k8stesting.Action) (bool, runtime.Object, error) {
				patched = true
				return false, nil, nil
			})
			s := NewSnapshot(&Client{dyn: dyn}, nil)
			if synced {
				_ = s.apps.Add(a)
				s.synced.Mark()
			}
			_, err := s.Resync(context.Background(), "alice", "prod", "vm-a")
			if tc.busy {
				if !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "already requested or running") {
					t.Errorf("%s, synced=%v: err = %v, want a conflict saying a sync is already running", tc.name, synced, err)
				}
				if patched {
					t.Errorf("%s, synced=%v: patched an Application whose operation is in flight", tc.name, synced)
				}
				continue
			}
			if err != nil {
				t.Errorf("%s, synced=%v: err = %v, want the resync to proceed", tc.name, synced, err)
			}
			if !patched {
				t.Errorf("%s, synced=%v: no patch sent", tc.name, synced)
			}
		}
	}
}

// The snapshot decides: an operation it has not seen yet does not block (the
// accepted window), and one it still shows blocks until the watch catches up.
func TestResyncReadsInFlightFromTheSnapshot(t *testing.T) {
	live := dotvirtApp("argocd", "team-a", "https://forge/team-a.git",
		[]any{vmResource("prod", "vm-a", "Synced", "Healthy")})
	live.Object["operation"] = map[string]any{"sync": map[string]any{"revision": "main"}}
	seen := dotvirtApp("argocd", "team-a", "https://forge/team-a.git",
		[]any{vmResource("prod", "vm-a", "Synced", "Healthy")})
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), live)
	gets := 0
	dyn.PrependReactor("*", "applications", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetVerb() == "get" || a.GetVerb() == "list" {
			gets++
		}
		return false, nil, nil
	})
	s := NewSnapshot(&Client{dyn: dyn}, nil)
	_ = s.apps.Add(seen)
	s.synced.Mark()
	if _, err := s.Resync(context.Background(), "alice", "prod", "vm-a"); err != nil {
		t.Errorf("snapshot shows no operation: err = %v, want the resync to proceed", err)
	}
	if gets != 0 {
		t.Errorf("a synced resync read the cluster %d times, want none", gets)
	}
}

// An Application tracking no explicit revision is synced at HEAD, Argo's own
// default; installs pin that value too.
func TestResyncWithoutTargetRevisionSyncsHEAD(t *testing.T) {
	a := dotvirtApp("argocd", "team-a", "https://forge/team-a.git",
		[]any{vmResource("prod", "vm-a", "Synced", "Healthy")})
	unstructured.RemoveNestedField(a.Object, "spec", "source", "targetRevision")
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), a)
	var sent []byte
	dyn.PrependReactor("patch", "applications", func(act k8stesting.Action) (bool, runtime.Object, error) {
		sent = act.(k8stesting.PatchAction).GetPatch()
		return false, nil, nil
	})
	s := NewSnapshot(&Client{dyn: dyn}, nil)
	_ = s.apps.Add(a)
	s.synced.Mark()
	res, err := s.Resync(context.Background(), "alice", "prod", "vm-a")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := resyncPatch("alice", "HEAD", "prod", "vm-a")
	if res.Revision != "HEAD" || string(sent) != string(want) {
		t.Errorf("revision = %q, patch = %s; want HEAD and %s", res.Revision, sent, want)
	}
}

// EmptyHold reads Argo's own refusal to sync a source that would prune every
// object it tracks, per repo folder, with the revisions the generator weighs;
// anything short of that predicate is no hold.
func TestEmptyHoldReadsArgosRefusal(t *testing.T) {
	const repo = "https://forge.example/dotvirt/team-a.git"
	res := func(name string, prune bool) map[string]any {
		return map[string]any{"kind": "VirtualMachine", "namespace": "team-a", "name": name, "requiresPruning": prune}
	}
	held := func(name, dir string, edit func(map[string]any)) *unstructured.Unstructured {
		a := appWithSource("argocd", name, repo, []any{res("web", true), res("db", true)})
		_ = unstructured.SetNestedField(a.Object, dir, "spec", "source", "path")
		_ = unstructured.SetNestedField(a.Object, true, "spec", "syncPolicy", "automated", "prune")
		_ = unstructured.SetNestedField(a.Object, "OutOfSync", "status", "sync", "status")
		_ = unstructured.SetNestedField(a.Object, "c2", "status", "sync", "revision")
		_ = unstructured.SetNestedSlice(a.Object, []any{map[string]any{"revision": "c0"}, map[string]any{"revision": "c1"}}, "status", "history")
		if edit != nil {
			edit(a.Object)
		}
		return a
	}
	s := NewSnapshot(nil, nil)
	_ = s.store().Replace([]any{
		held("dotvirt-team-a", ".", nil),
		held("dotvirt-declared", "declared", func(o map[string]any) {
			_ = unstructured.SetNestedSlice(o, []any{res("web", true), res("db", false)}, "status", "resources")
		}),
		held("dotvirt-manual", "manual", func(o map[string]any) {
			_ = unstructured.SetNestedField(o, false, "spec", "syncPolicy", "automated", "prune")
		}),
		held("dotvirt-synced", "synced", func(o map[string]any) {
			_ = unstructured.SetNestedField(o, "Synced", "status", "sync", "status")
		}),
		held("dotvirt-never", "never", func(o map[string]any) {
			unstructured.RemoveNestedField(o, "status", "history")
		}),
	}, "1")
	settle(s)
	if h, ok := s.EmptyHold(repo+"/", ""); !ok || h != (EmptyHold{Compared: "c2", Applied: "c1"}) {
		t.Errorf("held app: %+v %v, want compared c2 applied c1", h, ok)
	}
	for _, dir := range []string{"declared", "manual", "synced", "never", "elsewhere"} {
		if h, ok := s.EmptyHold(repo, dir); ok {
			t.Errorf("%s: read as held: %+v", dir, h)
		}
	}
}

// A folder whose app's prunes are on their way whatever merges next (a
// deliberate removal ArgoCD is letting through) is left out of what the next
// merge would delete; the repo's other apps still count.
func TestPrunePendingLeavesSettledFoldersOut(t *testing.T) {
	const repo = "https://forge.example/dotvirt/team-a.git"
	s := NewSnapshot(nil, nil)
	s.synced.Mark()
	res := func(ns, name string) map[string]any {
		return map[string]any{"kind": "VirtualMachine", "namespace": ns, "name": name, "requiresPruning": true}
	}
	root := appWithSource("argocd", "dotvirt-team-a", repo, []any{res("team-a", "web")})
	_ = unstructured.SetNestedField(root.Object, ".", "spec", "source", "path")
	folder := appWithSource("argocd", "dotvirt-team-c", repo, []any{res("team-c", "db")})
	_ = unstructured.SetNestedField(folder.Object, "team-c", "spec", "source", "path")
	for _, a := range []*unstructured.Unstructured{root, folder} {
		if err := s.apps.Add(a); err != nil {
			t.Fatal(err)
		}
	}
	want := []model.ObjectRef{{Kind: "VirtualMachine", Namespace: "team-c", Name: "db"}}
	if got, known := s.PrunePending(repo, ""); !known || !slices.Equal(got, want) {
		t.Fatalf("root settled: %v known=%v, want %v", got, known, want)
	}
}

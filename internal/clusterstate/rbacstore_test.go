package clusterstate

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/eventbus"
)

func meta(ns, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: ns, Name: name, ResourceVersion: "1"}
}

func user(name string) rbacv1.Subject { return rbacv1.Subject{Kind: rbacv1.UserKind, Name: name} }

func serviceAccount(ns, name string) rbacv1.Subject {
	return rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: ns, Name: name}
}

func roleBinding(ns, name, role string, subjects ...rbacv1.Subject) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{ObjectMeta: meta(ns, name), RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", Name: role}, Subjects: subjects}
}

func clusterRoleBinding(name, role string, subjects ...rbacv1.Subject) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{ObjectMeta: meta("", name), RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", Name: role}, Subjects: subjects}
}

func vmRules(verbs ...string) []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{{APIGroups: []string{"kubevirt.io"}, Resources: []string{"virtualmachines"}, Verbs: verbs}}
}

func group(name string, users ...any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "user.openshift.io/v1", "kind": "Group",
		"metadata": map[string]any{"name": name, "resourceVersion": "1"},
		"users":    users,
	}}
}

// grants are two versions of one object per watched kind, differing in what
// they grant.
func grants() map[string][2]runtime.Object {
	return map[string][2]runtime.Object{
		"rolebindings": {roleBinding("proj", "rb", "view", user("alice")), roleBinding("proj", "rb", "view", user("bob"))},
		"roles": {
			&rbacv1.Role{ObjectMeta: meta("proj", "r"), Rules: vmRules("get")},
			&rbacv1.Role{ObjectMeta: meta("proj", "r"), Rules: vmRules("get", "list")},
		},
		"clusterrolebindings": {clusterRoleBinding("crb", "view", user("alice")), clusterRoleBinding("crb", "edit", user("alice"))},
		"clusterroles": {
			&rbacv1.ClusterRole{ObjectMeta: meta("", "vm-reader"), Rules: vmRules("get")},
			&rbacv1.ClusterRole{ObjectMeta: meta("", "vm-reader"), Rules: vmRules("list")},
		},
		"groups": {group("admins", "alice"), group("admins", "alice", "bob")},
	}
}

// withProject is a State whose snapshot holds the project namespace "proj".
func withProject(t *testing.T) *State {
	t.Helper()
	s := stateWithIndexers()
	if err := s.nss.Add(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "proj"}}); err != nil {
		t.Fatal(err)
	}
	return s
}

// signalStores wires every RBAC sink New builds to one counter, by resource.
func signalStores(t *testing.T) (map[string]cache.Store, *int) {
	t.Helper()
	s := withProject(t)
	n := new(int)
	count := func() { *n++ }
	refs := newRoleRefs()
	stores := map[string]cache.Store{"rolebindings": newRBACSignal(count, refs, roleBindingDigest(s.inProject))}
	for _, w := range s.signalWatches(func(schema.GroupVersionResource) *cache.ListWatch { return nil }, count, refs) {
		stores[w.gvr.Resource] = w.store
	}
	return stores, n
}

// Every watched kind publishes when a grant appears, moves or goes, and only
// then: a metadata write and a relist that changed nothing stay silent.
func TestRBACSignalPublishesGrantMoves(t *testing.T) {
	for resource, g := range grants() {
		t.Run(resource, func(t *testing.T) {
			stores, n := signalStores(t)
			store := stores[resource]
			if resource == "clusterroles" {
				_ = stores["clusterrolebindings"].Add(clusterRoleBinding("readers", "vm-reader", user("carol")))
				*n = 0
			}
			step := func(what string, want int) {
				t.Helper()
				if *n != want {
					t.Fatalf("%s: %d publishes, want %d", what, *n, want)
				}
			}
			_ = store.Replace(nil, "1")
			step("initial list", 1)
			_ = store.Replace(nil, "2")
			step("relist of nothing", 1)
			_ = store.Add(g[0])
			step("add", 2)

			touched := g[0].DeepCopyObject()
			m, _ := apimeta.Accessor(touched)
			m.SetAnnotations(map[string]string{"touched": "yes"})
			m.SetResourceVersion("2")
			_ = store.Update(touched)
			step("metadata-only update", 2)

			_ = store.Update(g[1])
			step("grant update", 3)

			_ = store.Replace([]any{g[1].DeepCopyObject()}, "3")
			step("relist of nothing new", 3)
			_ = store.Replace([]any{g[0].DeepCopyObject()}, "4")
			step("relist that moved a grant", 4)

			key, _ := cache.MetaNamespaceKeyFunc(g[0])
			_ = store.Delete(cache.DeletedFinalStateUnknown{Key: key, Obj: g[0]})
			step("delete", 5)
		})
	}
}

// Churn no caller's authorization can see is dropped: bindings outside
// project namespaces or granting only the platform's own ServiceAccounts, and
// the system: roles. Editing a binding down to platform-only subjects is
// still a revocation and publishes.
func TestRBACSignalFiltersNoise(t *testing.T) {
	stores, n := signalStores(t)
	operator := serviceAccount("openshift-operators", "op")
	kube := serviceAccount("kube-system", "ctrl")

	_ = stores["rolebindings"].Add(roleBinding("other", "rb", "view", user("alice")))
	_ = stores["roles"].Add(&rbacv1.Role{ObjectMeta: meta("other", "r"), Rules: vmRules("get")})
	_ = stores["rolebindings"].Add(roleBinding("proj", "sys", "view", operator))
	_ = stores["clusterrolebindings"].Add(clusterRoleBinding("op", "admin", operator, kube))
	_ = stores["clusterroles"].Add(&rbacv1.ClusterRole{ObjectMeta: meta("", "system:controller:x"), Rules: vmRules("get")})
	_ = stores["clusterrolebindings"].Delete(clusterRoleBinding("op", "admin", operator))
	if *n != 0 {
		t.Fatalf("noise published %d times, want 0", *n)
	}

	// A tenant ServiceAccount is a dotvirt caller like any user.
	_ = stores["clusterrolebindings"].Add(clusterRoleBinding("ci", "view", serviceAccount("proj", "ci")))
	if *n != 1 {
		t.Fatalf("tenant ServiceAccount binding published %d times, want 1", *n)
	}
	_ = stores["clusterrolebindings"].Update(clusterRoleBinding("ci", "view", operator))
	if *n != 2 {
		t.Fatalf("revocation down to platform subjects published %d times in all, want 2", *n)
	}
	_ = stores["clusterrolebindings"].Update(clusterRoleBinding("ci", "view", operator, kube))
	if *n != 2 {
		t.Fatalf("platform-only edit published %d times in all, want 2", *n)
	}
}

// A ClusterRole signals only while a relevant binding of either kind binds
// it: the roles an operator writes for its own ServiceAccount stay silent.
func TestClusterRoleSignalsOnlyWhileBound(t *testing.T) {
	stores, n := signalStores(t)
	roles := stores["clusterroles"]
	role := func(verbs ...string) *rbacv1.ClusterRole {
		return &rbacv1.ClusterRole{ObjectMeta: meta("", "edit"), Rules: vmRules(verbs...)}
	}
	_ = roles.Add(role("get"))
	_ = stores["clusterrolebindings"].Add(clusterRoleBinding("op", "edit", serviceAccount("openshift-cnv", "op")))
	_ = roles.Update(role("list"))
	if *n != 0 {
		t.Fatalf("an unbound role published %d times, want 0", *n)
	}

	_ = stores["rolebindings"].Add(roleBinding("proj", "editors", "edit", user("alice")))
	*n = 0
	_ = roles.Update(role("get", "list"))
	if *n != 1 {
		t.Fatalf("a role a tenant binding binds published %d times, want 1", *n)
	}

	_ = stores["rolebindings"].Delete(roleBinding("proj", "editors", "edit", user("alice")))
	*n = 0
	_ = roles.Update(role("watch")) // ends its relevance: one last publish
	_ = roles.Update(role("get"))
	if *n != 1 {
		t.Fatalf("an unbound role published %d times after its last binding went, want 1", *n)
	}
}

// fakeSource is one kind's list and watch: the list counts calls, the watch is
// a fake the test pushes events through.
type fakeSource struct {
	lists atomic.Int32
	mu    sync.Mutex
	w     *watch.FakeWatcher
}

func (f *fakeSource) lw() *cache.ListWatch {
	return &cache.ListWatch{
		ListWithContextFunc: func(context.Context, metav1.ListOptions) (runtime.Object, error) {
			f.lists.Add(1)
			return &unstructured.UnstructuredList{Object: map[string]any{"metadata": map[string]any{"resourceVersion": "1"}}}, nil
		},
		WatchFuncWithContext: func(context.Context, metav1.ListOptions) (watch.Interface, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.w = watch.NewFake()
			return f.w, nil
		},
	}
}

func (f *fakeSource) watcher() *watch.FakeWatcher {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.w
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// runSignalsWith starts s's gated watches over fake sources, admitting the
// kinds served reports.
func runSignalsWith(t *testing.T, served func(schema.GroupVersionResource) bool) (*eventbus.Bus, map[string]*fakeSource) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bus := eventbus.New()
	s := withProject(t)
	refs := newRoleRefs()
	refs.hold("vm-reader") // as a relevant binding of it would
	sources := map[string]*fakeSource{}
	s.signals = s.signalWatches(func(gvr schema.GroupVersionResource) *cache.ListWatch {
		sources[gvr.Resource] = &fakeSource{}
		return sources[gvr.Resource].lw()
	}, func() { bus.Publish(eventbus.RBACChanged) }, refs)
	s.runSignals(ctx, served)
	return bus, sources
}

// pushPublishes drives add, update and delete through a running watch, each
// of which must publish.
func pushPublishes(t *testing.T, bus *eventbus.Bus, src *fakeSource, g [2]runtime.Object) {
	t.Helper()
	eventually(t, "the watch", func() bool { return src.watcher() != nil })
	w := src.watcher()
	for _, ev := range []struct {
		push func(runtime.Object)
		obj  runtime.Object
	}{{w.Add, g[0]}, {w.Modify, g[1]}, {w.Delete, g[1]}} {
		before := bus.Version(eventbus.RBACChanged)
		ev.push(ev.obj.DeepCopyObject())
		eventually(t, "a publish", func() bool { return bus.Version(eventbus.RBACChanged) > before })
	}
}

// Each gated watch reaches the bus through a real reflector, and Groups start
// only where the cluster serves them.
func TestSignalWatchesPublishAndGateGroups(t *testing.T) {
	bus, sources := runSignalsWith(t, func(gvr schema.GroupVersionResource) bool { return gvr != gvrGroups })
	for resource, g := range grants() {
		if resource == "rolebindings" || resource == "groups" {
			continue
		}
		pushPublishes(t, bus, sources[resource], g)
	}
	time.Sleep(10 * time.Millisecond)
	if got := sources["groups"].lists.Load(); got != 0 {
		t.Errorf("Groups listed %d times on a cluster that does not serve them", got)
	}
}

// Where the cluster serves Groups, a membership change publishes.
func TestGroupsWatchedWhenServed(t *testing.T) {
	bus, sources := runSignalsWith(t, func(gvr schema.GroupVersionResource) bool { return gvr == gvrGroups })
	pushPublishes(t, bus, sources["groups"], grants()["groups"])
}

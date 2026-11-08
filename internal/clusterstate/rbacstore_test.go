package clusterstate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// localBinding binds a Role of its own namespace.
func localBinding(ns, name, role string, subjects ...rbacv1.Subject) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{ObjectMeta: meta(ns, name), RoleRef: rbacv1.RoleRef{Kind: "Role", Name: role}, Subjects: subjects}
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

// published records the namespaces a sink published, "" for cluster-wide.
type published []string

func (p *published) record(ns string) { *p = append(*p, ns) }

// signalStores wires every RBAC sink New builds to one record, by resource.
func signalStores(t *testing.T) (map[string]cache.Store, *published) {
	t.Helper()
	p := &published{}
	refs := newRoleRefs()
	stores := map[string]cache.Store{"rolebindings": newRBACSignal(p.record, refs, roleBindingDigest)}
	for _, w := range signalWatches(func(schema.GroupVersionResource) *cache.ListWatch { return nil }, p.record, refs) {
		stores[w.gvr.Resource] = w.sink
	}
	return stores, p
}

// Every watched kind publishes when a grant appears, moves or goes, and only
// then: a metadata write and a relist that changed nothing stay silent. A
// RoleBinding or Role publishes its namespace, a cluster-scoped kind and any
// relist "" (every namespace).
func TestRBACSignalPublishesGrantMoves(t *testing.T) {
	for resource, g := range grants() {
		t.Run(resource, func(t *testing.T) {
			stores, p := signalStores(t)
			store := stores[resource]
			switch resource {
			case "clusterroles":
				_ = stores["clusterrolebindings"].Add(clusterRoleBinding("readers", "vm-reader", user("carol")))
			case "roles":
				_ = stores["rolebindings"].Add(localBinding("proj", "readers", "r", user("carol")))
			}
			*p = nil
			own := ""
			if resource == "rolebindings" || resource == "roles" {
				own = "proj"
			}
			var want published
			step := func(what string, ns ...string) {
				t.Helper()
				want = append(want, ns...)
				if !slices.Equal(*p, want) {
					t.Fatalf("%s: published %q, want %q", what, *p, want)
				}
			}
			_ = store.Replace(nil, "1")
			step("initial list", "")
			_ = store.Replace(nil, "2")
			step("relist of nothing")
			_ = store.Add(g[0])
			step("add", own)

			touched := g[0].DeepCopyObject()
			m, _ := apimeta.Accessor(touched)
			m.SetAnnotations(map[string]string{"touched": "yes"})
			m.SetResourceVersion("2")
			_ = store.Update(touched)
			step("metadata-only update")

			_ = store.Update(g[1])
			step("grant update", own)

			_ = store.Replace([]any{g[1].DeepCopyObject()}, "3")
			step("relist of nothing new")
			_ = store.Replace([]any{g[0].DeepCopyObject()}, "4")
			step("relist that moved a grant", "")

			key, _ := cache.MetaNamespaceKeyFunc(g[0])
			_ = store.Delete(cache.DeletedFinalStateUnknown{Key: key, Obj: g[0]})
			step("delete", own)
		})
	}
}

// Churn no caller's authorization can see is dropped: bindings granting only
// the platform's own ServiceAccounts, and the roles no other binding binds.
// Editing a binding down to platform-only subjects is still a revocation and
// publishes.
func TestRBACSignalFiltersNoise(t *testing.T) {
	stores, p := signalStores(t)
	operator := serviceAccount("openshift-operators", "op")
	kube := serviceAccount("kube-system", "ctrl")

	_ = stores["roles"].Add(&rbacv1.Role{ObjectMeta: meta("other", "r"), Rules: vmRules("get")})
	_ = stores["rolebindings"].Add(roleBinding("proj", "sys", "view", operator))
	_ = stores["rolebindings"].Add(localBinding("openshift-operators", "sys", "op-role", operator))
	_ = stores["roles"].Add(&rbacv1.Role{ObjectMeta: meta("openshift-operators", "op-role"), Rules: vmRules("get")})
	_ = stores["clusterrolebindings"].Add(clusterRoleBinding("op", "admin", operator, kube))
	_ = stores["clusterroles"].Add(&rbacv1.ClusterRole{ObjectMeta: meta("", "system:controller:x"), Rules: vmRules("get")})
	_ = stores["clusterrolebindings"].Delete(clusterRoleBinding("op", "admin", operator))
	if len(*p) != 0 {
		t.Fatalf("noise published %q, want nothing", *p)
	}

	// A tenant ServiceAccount is a dotvirt caller like any user.
	_ = stores["clusterrolebindings"].Add(clusterRoleBinding("ci", "view", serviceAccount("proj", "ci")))
	if len(*p) != 1 {
		t.Fatalf("tenant ServiceAccount binding published %q, want once", *p)
	}
	_ = stores["clusterrolebindings"].Update(clusterRoleBinding("ci", "view", operator))
	if len(*p) != 2 {
		t.Fatalf("revocation down to platform subjects published %q in all, want twice", *p)
	}
	_ = stores["clusterrolebindings"].Update(clusterRoleBinding("ci", "view", operator, kube))
	if len(*p) != 2 {
		t.Fatalf("platform-only edit published %q in all, want twice", *p)
	}
}

// A ClusterRole signals only while a relevant binding of either kind binds
// it: the roles an operator writes for its own ServiceAccount stay silent.
func TestClusterRoleSignalsOnlyWhileBound(t *testing.T) {
	stores, p := signalStores(t)
	roles := stores["clusterroles"]
	role := func(verbs ...string) *rbacv1.ClusterRole {
		return &rbacv1.ClusterRole{ObjectMeta: meta("", "edit"), Rules: vmRules(verbs...)}
	}
	_ = roles.Add(role("get"))
	_ = stores["clusterrolebindings"].Add(clusterRoleBinding("op", "edit", serviceAccount("openshift-cnv", "op")))
	_ = roles.Update(role("list"))
	if len(*p) != 0 {
		t.Fatalf("an unbound role published %q, want nothing", *p)
	}

	_ = stores["rolebindings"].Add(roleBinding("proj", "editors", "edit", user("alice")))
	*p = nil
	_ = roles.Update(role("get", "list"))
	if !slices.Equal(*p, published{""}) {
		t.Fatalf("a role a tenant binding binds published %q, want once cluster-wide", *p)
	}

	_ = stores["rolebindings"].Delete(roleBinding("proj", "editors", "edit", user("alice")))
	*p = nil
	_ = roles.Update(role("watch")) // ends its relevance: one last publish
	_ = roles.Update(role("get"))
	if len(*p) != 1 {
		t.Fatalf("an unbound role published %q after its last binding went, want once", *p)
	}
}

// A Role signals, in its namespace, only while a relevant RoleBinding there
// binds it: the Roles an operator writes for its own ServiceAccount stay
// silent.
func TestRoleSignalsOnlyWhileBound(t *testing.T) {
	stores, p := signalStores(t)
	roles := stores["roles"]
	role := func(ns string, verbs ...string) *rbacv1.Role {
		return &rbacv1.Role{ObjectMeta: meta(ns, "vm"), Rules: vmRules(verbs...)}
	}
	_ = roles.Add(role("x", "get"))
	_ = stores["rolebindings"].Add(localBinding("x", "op", "vm", serviceAccount("openshift-cnv", "op")))
	_ = stores["rolebindings"].Add(localBinding("y", "alice", "vm", user("alice")))
	*p = nil
	_ = roles.Update(role("x", "list"))
	if len(*p) != 0 {
		t.Fatalf("a Role bound only in another namespace published %q, want nothing", *p)
	}
	_ = stores["rolebindings"].Add(localBinding("x", "bob", "vm", user("bob")))
	*p = nil
	_ = roles.Update(role("x", "get", "list"))
	if !slices.Equal(*p, published{"x"}) {
		t.Fatalf("a bound Role published %q, want [x]", *p)
	}
}

// A binding outside any project publishes, keyed by its namespace: a review
// there may be cached (a platform kind's create in an operator namespace),
// and a keyed move no longer re-asks anything elsewhere. Its ClusterRole's
// edits publish cluster-wide.
func TestBindingOutsideAProjectPublishesItsNamespace(t *testing.T) {
	stores, p := signalStores(t)
	rb, cr := stores["rolebindings"], stores["clusterroles"]
	_ = rb.Replace([]any{roleBinding("openshift-kube-descheduler-operator", "alice-desched", "desched-admin", user("alice"))}, "1")
	_ = cr.Replace([]any{&rbacv1.ClusterRole{ObjectMeta: meta("", "desched-admin"), Rules: vmRules("get", "list", "update")}}, "1")
	*p = nil
	_ = rb.Update(roleBinding("openshift-kube-descheduler-operator", "alice-desched", "desched-admin", user("bob")))
	_ = cr.Update(&rbacv1.ClusterRole{ObjectMeta: meta("", "desched-admin")})
	if want := (published{"openshift-kube-descheduler-operator", ""}); !slices.Equal(*p, want) {
		t.Fatalf("published %q, want %q", *p, want)
	}
}

// Editing a binding down to no caller, or deleting it, is a revocation and
// publishes its namespace; a relist that drops it publishes cluster-wide.
func TestBindingRevocationsPublish(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*rbacSignal) error
		want string
	}{
		"no subjects": {func(rb *rbacSignal) error { return rb.Update(roleBinding("proj", "b", "vm-admin")) }, "proj"},
		"platform only": {func(rb *rbacSignal) error {
			return rb.Update(roleBinding("proj", "b", "vm-admin", serviceAccount("openshift-cnv", "op")))
		}, "proj"},
		"deleted":       {func(rb *rbacSignal) error { return rb.Delete(roleBinding("proj", "b", "vm-admin", user("alice"))) }, "proj"},
		"relisted away": {func(rb *rbacSignal) error { return rb.Replace(nil, "2") }, ""},
	} {
		t.Run(name, func(t *testing.T) {
			p := &published{}
			rb := newRBACSignal(p.record, newRoleRefs(), roleBindingDigest)
			_ = rb.Replace([]any{roleBinding("proj", "b", "vm-admin", user("alice"))}, "1")
			*p = nil
			_ = tc.edit(rb)
			if !slices.Equal(*p, published{tc.want}) {
				t.Errorf("the revocation published %q, want [%q]", *p, tc.want)
			}
		})
	}
}

// A binding moving within one role never leaves that role unbound, even for
// an instant the ClusterRole sink could observe from its own reflector.
func TestRoleStaysBoundAcrossABindingUpdate(t *testing.T) {
	refs := newRoleRefs()
	rb := newRBACSignal(func(string) {}, refs, roleBindingDigest)
	_ = rb.Add(roleBinding("proj", "b", "vm-admin", user("alice")))
	done := make(chan struct{})
	var unbound atomic.Bool
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				if !refs.bound("vm-admin") {
					unbound.Store(true)
				}
			}
		}
	}()
	for i := range 20000 {
		_ = rb.Update(roleBinding("proj", "b", "vm-admin", user(fmt.Sprint("user-", i))))
		if i%1000 == 0 {
			_ = rb.Replace([]any{roleBinding("proj", "b", "vm-admin", user("carol"))}, "x")
		}
	}
	close(done)
	if unbound.Load() {
		t.Fatal("the role read unbound while its binding only moved")
	}
}

// A namespace's ServiceAccount group is the platform's when the namespace is.
func TestPlatformGroupBindingsAreNoise(t *testing.T) {
	stores, p := signalStores(t)
	_ = stores["clusterrolebindings"].Add(clusterRoleBinding("pullers", "view",
		rbacv1.Subject{Kind: rbacv1.GroupKind, Name: "system:serviceaccounts:openshift-monitoring"}))
	if len(*p) != 0 {
		t.Fatalf("a platform group binding published %q, want nothing", *p)
	}
	_ = stores["clusterrolebindings"].Add(clusterRoleBinding("tenants", "view",
		rbacv1.Subject{Kind: rbacv1.GroupKind, Name: "system:serviceaccounts:proj"}))
	if len(*p) != 1 {
		t.Fatalf("a tenant group binding published %q, want once", *p)
	}
}

// fakeSource is one kind's list and watch: the list counts calls, the watch is
// a fake the test pushes events through, and fail makes both refuse.
type fakeSource struct {
	lists atomic.Int32
	fail  atomic.Bool
	mu    sync.Mutex
	w     *watch.FakeWatcher
}

var errSource = errors.New("source down")

func (f *fakeSource) lw() *cache.ListWatch {
	return &cache.ListWatch{
		ListWithContextFunc: func(context.Context, metav1.ListOptions) (runtime.Object, error) {
			if f.fail.Load() {
				return nil, errSource
			}
			f.lists.Add(1)
			return &unstructured.UnstructuredList{Object: map[string]any{"metadata": map[string]any{"resourceVersion": "1"}}}, nil
		},
		WatchFuncWithContext: func(context.Context, metav1.ListOptions) (watch.Interface, error) {
			if f.fail.Load() {
				return nil, errSource
			}
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

// servesAllBut is a gate admitting every kind except the one this cluster
// does not serve.
func servesAllBut(missing schema.GroupVersionResource) func(schema.GroupVersionResource) admission {
	return func(gvr schema.GroupVersionResource) admission {
		if gvr == missing {
			return absent
		}
		return admitted
	}
}

// runSignalsWith starts s's gated watches over fake sources behind gate.
func runSignalsWith(t *testing.T, gate func(schema.GroupVersionResource) admission) (*eventbus.Bus, map[string]*fakeSource, *State) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bus := eventbus.New()
	s := stateWithIndexers()
	refs := newRoleRefs()
	refs.hold("vm-reader")          // as a relevant binding of it would
	refs.hold(roleKey("proj", "r")) // likewise
	sources := map[string]*fakeSource{}
	s.signals = signalWatches(func(gvr schema.GroupVersionResource) *cache.ListWatch {
		sources[gvr.Resource] = &fakeSource{}
		return sources[gvr.Resource].lw()
	}, func(string) { bus.Publish(eventbus.RBACChanged) }, refs)
	s.runSignals(ctx, gate)
	return bus, sources, s
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
	bus, sources, _ := runSignalsWith(t, servesAllBut(gvrGroups))
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
	bus, sources, _ := runSignalsWith(t, servesAllBut(schema.GroupVersionResource{}))
	pushPublishes(t, bus, sources["groups"], grants()["groups"])
}

// The authorization caches may keep entries for minutes only while every RBAC
// watch has listed and is watching: not while one is still listing, not while
// one has failed, not while a kind waits for its grant. A kind the cluster
// does not serve has nothing to miss.
func TestRBACWatched(t *testing.T) {
	_, sources, s := runSignalsWith(t, servesAllBut(gvrGroups))
	s.rbSink = newRBACSignal(func(string) {}, nil, roleBindingDigest)
	s.healthy, s.rbHealth = make([]atomic.Bool, 1), 0
	for _, src := range sources {
		if src != sources["groups"] {
			eventually(t, "the watches", func() bool { return src.watcher() != nil })
		}
	}
	if s.RBACWatched() {
		t.Fatal("watched while the RoleBinding watch has not listed")
	}
	_ = s.rbSink.Replace(nil, "1")
	s.healthy[0].Store(true)
	eventually(t, "every watch running", s.RBACWatched)

	roles := sources["clusterroles"]
	roles.fail.Store(true)
	roles.watcher().Stop()
	eventually(t, "a failed watch to count", func() bool { return !s.RBACWatched() })
	roles.fail.Store(false)
	eventually(t, "the recovered watch to count", s.RBACWatched)

	s.healthy[0].Store(false)
	if s.RBACWatched() {
		t.Error("watched while the RoleBinding watch is down")
	}
	s.healthy[0].Store(true)

	_, _, denied := runSignalsWith(t, func(gvr schema.GroupVersionResource) admission {
		if gvr == gvrClusterRoles {
			return notYet
		}
		return admitted
	})
	denied.rbSink, denied.healthy, denied.rbHealth = s.rbSink, s.healthy, 0
	time.Sleep(50 * time.Millisecond)
	if denied.RBACWatched() {
		t.Error("watched while a kind waits for its grant")
	}
}

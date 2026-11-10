package api

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/restfactory"
)

// grantRig is a server over a snapshot whose RBAC watches run on a fake
// clientset, so a binding written there reaches the caches the way a real
// grant move does: through the signal watch and the bus.
type grantRig struct {
	t    *testing.T
	s    *Server
	bus  *eventbus.Bus
	kube *fake.Clientset
}

func newGrantRig(t *testing.T, projects []string, objs ...runtime.Object) *grantRig {
	t.Helper()
	for _, p := range projects {
		objs = append(objs, projectNS(p))
	}
	bus := eventbus.New()
	st, kube, _ := watchedState(t, bus, objs...)
	return &grantRig{t: t, s: NewServer(Deps{State: st, Bus: bus, Reader: &fakeDraft{}, Draft: &fakeDraft{}}), bus: bus, kube: kube}
}

func userBinding(ns, name, user string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "edit"},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: user}},
	}
}

// bind writes a RoleBinding in ns, or a ClusterRoleBinding when ns is "",
// and waits for its watch to publish it.
func (g *grantRig) bind(ns, name string) {
	g.t.Helper()
	ctx := context.Background()
	seen := g.grantLevels()
	var err error
	if ns == "" {
		_, err = g.kube.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "view"},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "carol"}},
		}, metav1.CreateOptions{})
	} else {
		_, err = g.kube.RbacV1().RoleBindings(ns).Create(ctx, userBinding(ns, name, "carol"), metav1.CreateOptions{})
	}
	if err != nil {
		g.t.Fatal(err)
	}
	waitUntil(g.t, "the binding's publish", func() bool { return g.grantLevels() != seen })
}

func (g *grantRig) grantLevels() [3]uint64 {
	return [3]uint64{
		g.bus.KeySeq(eventbus.RBACChanged),
		g.bus.KeySeq(eventbus.OtherRBACChanged),
		g.bus.BroadVersion(eventbus.RBACChanged),
	}
}

// vmUpdate is the per-namespace question a frame asks of each namespace.
func vmUpdate(ns string) question {
	return question{Verb: verbUpdate, Group: "kubevirt.io", Resource: "virtualmachines", Namespace: ns}
}

func (g *grantRig) ask(tok string, a *asking, q question) bool {
	g.t.Helper()
	ok, err := g.s.can(context.Background(), auth.Identity{Token: tok, Username: tok}, a.c, q)
	if err != nil {
		g.t.Fatalf("can(%s): %v", tok, err)
	}
	return ok
}

// A RoleBinding moving in one namespace re-asks, for every token, only the
// questions about that namespace and only its visibility; a
// ClusterRoleBinding moving re-asks everything.
func TestGrantMoveReasksOnlyItsNamespace(t *testing.T) {
	g := newGrantRig(t, []string{"p0", "p1", "p2"})
	tokens := map[string]*asking{}
	for i := range 20 {
		tokens[fmt.Sprint("tok-", i)] = newAsking(readsVMsIn("p0", "p1"))
	}
	read := func() {
		for tok, a := range tokens {
			visibleOf(t, g.s, tok, a.c)
			g.ask(tok, a, vmUpdate("p0"))
			g.ask(tok, a, vmUpdate("p1"))
		}
	}
	read()
	for _, a := range tokens {
		a.take()
	}

	g.bind("p1", "carol-edit")
	read()
	for tok, a := range tokens {
		if got := a.take(); !slices.Equal(got, slices.Repeat([]string{"p1"}, 2+len(readKinds))) {
			t.Fatalf("%s after a move in p1 asked %q, want p1's visibility, its reads and p1's question", tok, got)
		}
	}

	g.bind("", "carol-view")
	read()
	for tok, a := range tokens {
		if got := a.where(); !slices.Equal(got, []string{"", "p0", "p1", "p2"}) {
			t.Fatalf("%s after a cluster-wide move asked %q, want everything again", tok, got)
		}
	}
}

// A revocation outside every project namespace still invalidates the
// verdict cached there: a platform kind's create in an operator namespace is
// asked in that namespace, and its RoleBinding is watched like any other.
func TestRevokeOutsideAProjectInvalidates(t *testing.T) {
	const ns = "openshift-kube-descheduler-operator"
	g := newGrantRig(t, []string{"proj"}, userBinding(ns, "alice-desched", "alice"))
	var granted atomic.Bool
	granted.Store(true)
	alice := newAsking(func(a *authzv1.ResourceAttributes) bool {
		return granted.Load() && a.Namespace == ns && a.Resource == ssarDescheduler.resource
	})
	q := refQ(verbCreate, ssarDescheduler, ns)
	if !g.ask("tok-alice", alice, q) {
		t.Fatal("the grant reads denied")
	}
	if g.ask("tok-alice", alice, q); len(alice.take()) != 1 {
		t.Fatal("the verdict was not cached")
	}

	granted.Store(false)
	if err := g.kube.RbacV1().RoleBindings(ns).Delete(context.Background(), "alice-desched", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the revocation to reach the cached verdict", func() bool { return !g.ask("tok-alice", alice, q) })
}

// A new project namespace is the only one a view reviews, and one that
// stops being a project leaves the set with no review at all.
func TestNamespaceMovesAreDeltas(t *testing.T) {
	g := newGrantRig(t, []string{"a", "b"})
	tenant := newAsking(readsVMsIn("a", "b", "c"))
	if got := visibleOf(t, g.s, "tok", tenant.c); !maps.Equal(got, map[string]bool{"a": true, "b": true}) {
		t.Fatalf("visible = %v", got)
	}
	tenant.take()

	ctx := context.Background()
	if _, err := g.kube.CoreV1().Namespaces().Create(ctx, projectNS("c"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "c to become a candidate", func() bool { return len(g.s.state.Namespaces()) == 3 })
	if got := visibleOf(t, g.s, "tok", tenant.c); !maps.Equal(got, map[string]bool{"a": true, "b": true, "c": true}) {
		t.Fatalf("after c was added: visible = %v", got)
	}
	if got := tenant.where(); !slices.Equal(got, []string{"c"}) {
		t.Fatalf("adding c asked %q, want c alone", got)
	}

	if err := g.kube.CoreV1().Namespaces().Delete(ctx, "a", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "a to leave the candidates", func() bool { return len(g.s.state.Namespaces()) == 2 })
	if got := visibleOf(t, g.s, "tok", tenant.c); !maps.Equal(got, map[string]bool{"b": true, "c": true}) {
		t.Fatalf("after a was removed: visible = %v", got)
	}
	if got := tenant.take(); len(got) != 0 {
		t.Fatalf("removing a asked %q, want nothing", got)
	}
}

// A partial re-review that fails is never cached, and never leaves the
// failed namespace's old verdict serving: the read fails until the review
// answers, then reads the new grant.
func TestFailedPartialReviewIsNotCached(t *testing.T) {
	g := newGrantRig(t, []string{"x", "y", "z"})
	tenant := newAsking(readsVMsIn("x", "y"))
	visibleOf(t, g.s, "tok", tenant.c)

	tenant.set(readsVMsIn("x"), func(a *authzv1.ResourceAttributes) bool { return a.Namespace == "y" })
	g.bus.PublishKey(eventbus.RBACChanged, "x")
	g.bus.PublishKey(eventbus.RBACChanged, "y")
	id := auth.Identity{Token: "tok", Username: "tok"}
	for range 2 {
		if got, err := g.s.visibleFor(context.Background(), id, tenant.c); err == nil {
			t.Fatalf("a failed review of y answered %v", got)
		}
	}
	tenant.set(readsVMsIn("x"), nil)
	tenant.take()
	if got := visibleOf(t, g.s, "tok", tenant.c); !maps.Equal(got, map[string]bool{"x": true}) {
		t.Fatalf("after recovery: visible = %v, want x alone (y's old verdict must not survive)", got)
	}
	if got := tenant.take(); !slices.Contains(got, "y") {
		t.Fatalf("recovery asked %q, want y asked again", got)
	}
}

// The request cost of one grant move at N tokens over M project namespaces:
// a RoleBinding in one project namespace costs a review per token plus one per
// read kind for those who see it, one outside the projects none, and a
// ClusterRoleBinding a sweep per token, one cluster-wide review, one per
// namespace and one per read kind per namespace seen. A sweep per token is what a
// RoleBinding in a project namespace cost before (a namespace list and a
// review per namespace), each repeated once its unsettled answers expired.
func TestReviewsPerGrantMove(t *testing.T) {
	const n, m = 50, 20
	projects := make([]string, m)
	for i := range projects {
		projects[i] = fmt.Sprintf("p%02d", i)
	}
	g := newGrantRig(t, projects)
	tokens := make([]*asking, n)
	for i := range tokens {
		tokens[i] = newAsking(readsVMsIn(projects[i%m]))
	}
	cost := func() int {
		total := 0
		for i, a := range tokens {
			visibleOf(t, g.s, fmt.Sprint("tok-", i), a.c)
			total += len(a.take())
		}
		return total
	}
	sweep := n * (1 + m + len(readKinds))
	if got := cost(); got != sweep {
		t.Fatalf("first reads cost %d reviews, want %d", got, sweep)
	}
	g.bind("p03", "rb")
	keyed := cost()
	g.bind("ci-builds", "rb")
	outside := cost()
	g.bind("", "crb")
	wide := cost()
	t.Logf("reviews per grant move at %d tokens x %d namespaces: RoleBinding in a project %d, outside %d, ClusterRoleBinding %d",
		n, m, keyed, outside, wide)
	seers := n / m
	if n%m > 3 {
		seers++
	}
	if want := n + seers*len(readKinds); keyed != want || outside != 0 || wide != sweep {
		t.Fatalf("got %d, %d, %d reviews; want %d, 0, %d", keyed, outside, wide, want, sweep)
	}
}

// A dry run's verdict stands while the grants of every namespace it asks in
// stand: its own, and for a Namespace object the namespace itself, where its
// update is asked.
func TestDryRunVerdictFollowsItsNamespaces(t *testing.T) {
	bus := eventbus.New()
	s := scopeServer(bus)
	id := auth.Identity{Token: "tok", Username: "tok"}
	weighed := 0
	weigh := func(context.Context, auth.Identity, *cluster.Client, changeset.Access) (bool, error) {
		weighed++
		return true, nil
	}
	for _, tc := range []struct {
		name string
		a    changeset.Access
	}{
		{"a VM in x", changeset.Access{Verb: verbDryRun, Group: "kubevirt.io", Resource: "virtualmachines", Namespace: "x", Name: "web", Object: `{"kind":"VirtualMachine"}`}},
		{"the Namespace x", changeset.Access{Verb: verbDryRun, Resource: "namespaces", Name: "x", Object: `{"kind":"Namespace"}`}},
	} {
		weighed = 0
		step := func(what string, want int) {
			t.Helper()
			if _, err := s.dryRun(context.Background(), id, nil, tc.a, weigh); err != nil {
				t.Fatal(err)
			}
			if weighed != want {
				t.Fatalf("%s, %s: weighed %d times, want %d", tc.name, what, weighed, want)
			}
		}
		step("first", 1)
		step("cached", 1)
		bus.PublishKey(eventbus.RBACChanged, "y")
		step("after a move in y", 1)
		bus.PublishKey(eventbus.RBACChanged, "x")
		step("after a move in x", 2)
		bus.Publish(eventbus.RBACChanged)
		step("after a cluster-wide move", 3)
	}
}

// A question about one Namespace object is asked in that namespace, so a
// move there re-asks it and a move elsewhere does not.
func TestNamespaceObjectQuestionFollowsItsNamespace(t *testing.T) {
	bus := eventbus.New()
	s := scopeServer(bus)
	a := newAsking(func(*authzv1.ResourceAttributes) bool { return true })
	id := auth.Identity{Token: "tok", Username: "tok"}
	q := question{Verb: verbPatch, Resource: ssarNamespace.resource, Name: "x"}
	step := func(what string, want ...string) {
		t.Helper()
		if ok, err := s.ask(context.Background(), id, a.c, q); !ok || err != nil {
			t.Fatalf("%s: %v, %v", what, ok, err)
		}
		if got := a.take(); !slices.Equal(got, want) {
			t.Fatalf("%s: asked in %q, want %q", what, got, want)
		}
	}
	step("first", "x")
	bus.PublishKey(eventbus.RBACChanged, "y")
	step("after a move in y")
	bus.PublishKey(eventbus.RBACChanged, "x")
	step("after a move in x", "x")
}

// hubKinds are the kinds the inventory hub wakes on and stamps frames with
// (cmd/dotvirt).
var hubKinds = []eventbus.Kind{
	eventbus.LiveChanged, eventbus.NamespaceChanged,
	eventbus.RBACChanged, eventbus.DriftChanged, eventbus.GitChanged, eventbus.ProposalsChanged,
	eventbus.NetworkChanged, eventbus.TaskChanged, eventbus.TTLBackstop,
}

// A grant move outside every project namespace wakes no stream and moves no
// frame's stamp, nor the backstop's period: a frame asks only of project
// namespaces and cluster scope. One in a project namespace wakes the hub.
func TestGrantMoveOutsideProjectsWakesNoFrame(t *testing.T) {
	g := newGrantRig(t, []string{"p0"})
	wake, cancel := g.bus.Subscribe(hubKinds...)
	defer cancel()
	select {
	case <-wake:
	default:
	}
	stamp, moved := g.bus.Version(hubKinds...), g.s.state.LastGrantMove()

	g.bind("ci-builds", "rb")
	select {
	case <-wake:
		t.Error("a grant move outside the projects woke the hub")
	default:
	}
	if g.bus.Version(hubKinds...) != stamp {
		t.Error("a grant move outside the projects moved the frames' stamp")
	}
	if !g.s.state.LastGrantMove().Equal(moved) {
		t.Error("a grant move outside the projects shortened the backstop period")
	}

	g.bind("p0", "rb")
	select {
	case <-wake:
	default:
		t.Error("a grant move in a project namespace did not wake the hub")
	}
}

// A token that may list every read kind everywhere sees a new project namespace
// with no review, and stops seeing a removed one.
func TestWideReaderFollowsTheProjects(t *testing.T) {
	g := newGrantRig(t, []string{"a"})
	wide := newAsking(func(*authzv1.ResourceAttributes) bool { return true })
	visibleOf(t, g.s, "tok", wide.c)
	wide.take()
	if _, err := g.kube.CoreV1().Namespaces().Create(context.Background(), projectNS("b"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "b to become a candidate", func() bool { return len(g.s.state.Namespaces()) == 2 })
	if got := visibleOf(t, g.s, "tok", wide.c); !maps.Equal(got, map[string]bool{"a": true, "b": true}) {
		t.Fatalf("after b was added: visible = %v", got)
	}
	if got := wide.take(); len(got) != 0 {
		t.Fatalf("adding b asked %q of a cluster-wide reader", got)
	}
}

// A review right after a grant move in its namespace serves the short TTL,
// and one in another namespace the long one: the view marks the moved
// namespace alone, re-asks it once that TTL ran out, and a verdict in it is
// cached unsettled while one elsewhere is not. It waits out the settle window
// of the snapshot's own first lists, which move every grant.
func TestUnsettledIsPerNamespace(t *testing.T) {
	t.Parallel()
	g := newGrantRig(t, []string{"x", "y"})
	waitFor := grantSettle - time.Since(g.s.state.LastGrantMoveFor(""))
	time.Sleep(waitFor + 100*time.Millisecond)
	tenant := newAsking(readsVMsIn("x", "y"))
	visibleOf(t, g.s, "tok", tenant.c)
	key := restfactory.TokenKey("tok")
	if m, _ := g.s.views.Get(key); m.until.Sub(m.built) <= unwatchedTTL {
		t.Fatalf("a sweep long after any move serves %s", m.until.Sub(m.built))
	}

	g.bind("x", "rb")
	tenant.take()
	visibleOf(t, g.s, "tok", tenant.c)
	if got := tenant.take(); !slices.Equal(got, slices.Repeat([]string{"x"}, 1+len(readKinds))) {
		t.Fatalf("a move in x asked %q", got)
	}
	if m, _ := g.s.views.Get(key); m.until.Sub(m.built) > unwatchedTTL+time.Second {
		t.Fatalf("after a move in x the view serves %s, want x's short TTL", m.until.Sub(m.built))
	}

	ageVerdicts(g.s, "tok", unwatchedTTL, nsQuestions("x")...)
	visibleOf(t, g.s, "tok", tenant.c)
	if got := tenant.where(); !slices.Equal(got, []string{"x"}) {
		t.Fatalf("x's short TTL ran out, yet the read asked %q", got)
	}

	for ns, want := range map[string]bool{"x": true, "y": false} {
		q := vmUpdate(ns)
		g.ask("tok", tenant, q)
		k := verdictKey(restfactory.TokenKey("tok"), q)
		if v, ok := g.s.ssar.Get(k); !ok || v.unsettled != want {
			t.Errorf("a verdict in %s cached unsettled=%v (ok=%v), want %v", ns, v.unsettled, ok, want)
		}
	}
}

// A stamp survives no move of its namespace, forgotten between or not: the
// verdict cached after a move in X is re-asked once X is forgotten (its
// namespace deleted) and moves again, and once a cluster-wide move follows a
// forget, which a summed stamp could re-equal.
func TestForgottenNamespaceNeverRevalidates(t *testing.T) {
	for name, after := range map[string]func(*eventbus.Bus){
		"recreated and moved":      func(b *eventbus.Bus) { b.PublishKey(eventbus.RBACChanged, "x") },
		"then a cluster-wide move": func(b *eventbus.Bus) { b.Publish(eventbus.RBACChanged) },
	} {
		t.Run(name, func(t *testing.T) {
			bus := eventbus.New()
			s := scopeServer(bus)
			var granted atomic.Bool
			granted.Store(true)
			alice := newAsking(func(*authzv1.ResourceAttributes) bool { return granted.Load() })
			bus.PublishKey(eventbus.RBACChanged, "x")
			q := vmUpdate("x")
			if !(&grantRig{t: t, s: s}).ask("tok", alice, q) {
				t.Fatal("the grant reads denied")
			}
			granted.Store(false)
			bus.Forget(eventbus.RBACChanged, "x")
			bus.Forget(eventbus.OtherRBACChanged, "x")
			after(bus)
			if (&grantRig{t: t, s: s}).ask("tok", alice, q) {
				t.Fatal("the cached allow revalidated across the forget")
			}
		})
	}
}

// relabel makes ns a project or not, and waits for the snapshot to agree.
func relabel(t *testing.T, g *grantRig, ns string, project bool) {
	t.Helper()
	obj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
	if project {
		obj.Labels = map[string]string{"dotvirt.io/project": ns}
	}
	if _, err := g.kube.CoreV1().Namespaces().Update(context.Background(), obj, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the label to reach the store", func() bool { return g.s.state.Holds("", "namespaces", "", ns) == project })
}

// A namespace leaves the projects, its grant is revoked there (published as
// OtherRBACChanged), and it comes back: with or without a read between,
// neither the view nor a verdict cached in it keeps the old allow.
func TestCrossKindRevocation(t *testing.T) {
	for _, between := range []bool{false, true} {
		g := newGrantRig(t, []string{"z", "y"}, userBinding("z", "carol-edit", "carol"))
		tenant := newAsking(readsVMsIn("z", "y"))
		visibleOf(t, g.s, "tok", tenant.c)
		q := question{Verb: verbList, Group: "kubevirt.io", Resource: "virtualmachines", Namespace: "z"}
		if !g.ask("tok", tenant, q) {
			t.Fatal("the grant reads denied")
		}
		relabel(t, g, "z", false)
		if between {
			visibleOf(t, g.s, "tok", tenant.c)
		}
		other := g.bus.KeySeq(eventbus.OtherRBACChanged)
		tenant.set(readsVMsIn("y"), nil)
		if err := g.kube.RbacV1().RoleBindings("z").Delete(context.Background(), "carol-edit", metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		waitUntil(t, "the revocation's publish", func() bool { return g.bus.KeySeq(eventbus.OtherRBACChanged) != other })
		relabel(t, g, "z", true)
		if got := visibleOf(t, g.s, "tok", tenant.c); got["z"] {
			t.Errorf("between=%v: the view kept the revoked z: %v", between, got)
		}
		if g.ask("tok", tenant, q) {
			t.Errorf("between=%v: the verdict cached in z kept the revoked allow", between)
		}
	}
}

// A forgotten namespace's marks read 0 again (eventbus.Forget), which a stamp
// taken before its last move could re-equal: every stamp of a mark must have
// expired by then. The caches holding them (visible, ssar, dry runs in ssar)
// evict at visibleTTL, verdicts serve at most that or unwatchedTTL, and an
// open frame re-reads within the backstop bound of either.
func TestForgetOutlivesEveryGrantStamp(t *testing.T) {
	for name, ttl := range map[string]time.Duration{
		"cache eviction and the long TTL": visibleTTL,
		"the short TTL and dry runs":      unwatchedTTL,
		"the long backstop bound":         backstopBound(visibleTTL),
		"the short backstop bound":        backstopBound(unwatchedTTL),
	} {
		if clusterstate.ForgetAfter <= ttl {
			t.Errorf("ForgetAfter %s does not outlive %s (%s)", clusterstate.ForgetAfter, name, ttl)
		}
	}
}

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"
	kvfake "kubevirt.io/client-go/kubevirt/fake"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/modules"
	"github.com/epheo/dotvirt/internal/restfactory"
)

// scopeServer builds a Server with only what the scope caches read: the version
// bus and an unsynced snapshot, which holds no namespace.
func scopeServer(bus *eventbus.Bus) *Server {
	sa := cluster.NewClient(fake.NewSimpleClientset(), nil, nil)
	return NewServer(Deps{State: clusterstate.New(sa, "dotvirt.io/project", bus), Bus: bus, Reader: &fakeDraft{}, Draft: &fakeDraft{}})
}

// ssarCountingClient answers SelfSubjectAccessReviews via allow and counts them.
func ssarCountingClient(allow func(*authzv1.ResourceAttributes) bool) (*cluster.Client, *int) {
	c, _, reviews := ssarCountingFake(allow)
	return c, reviews
}

// ssarCountingFake is ssarCountingClient with its clientset, whose Actions
// record every other request the token made.
func ssarCountingFake(allow func(*authzv1.ResourceAttributes) bool) (*cluster.Client, *fake.Clientset, *int) {
	kube := fake.NewSimpleClientset()
	reviews := new(int)
	kube.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		*reviews++
		ssar := action.(k8stesting.CreateAction).GetObject().(*authzv1.SelfSubjectAccessReview).DeepCopy()
		ssar.Status.Allowed = allow(ssar.Spec.ResourceAttributes)
		return true, ssar, nil
	})
	return cluster.NewClient(kube, nil, nil), kube, reviews
}

// asking is a token's client that records the namespace of every review it
// posts ("" for cluster-wide), allowing those allow does.
type asking struct {
	c     *cluster.Client
	mu    sync.Mutex
	asked []string
	allow func(*authzv1.ResourceAttributes) bool
	fail  func(*authzv1.ResourceAttributes) bool
}

func newAsking(allow func(*authzv1.ResourceAttributes) bool) *asking {
	a := &asking{allow: allow}
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ssar := action.(k8stesting.CreateAction).GetObject().(*authzv1.SelfSubjectAccessReview).DeepCopy()
		attrs := ssar.Spec.ResourceAttributes
		a.mu.Lock()
		defer a.mu.Unlock()
		a.asked = append(a.asked, attrs.Namespace)
		if a.fail != nil && a.fail(attrs) {
			return true, nil, apierrors.NewServiceUnavailable("review backend down")
		}
		ssar.Status.Allowed = a.allow(attrs)
		return true, ssar, nil
	})
	a.c = cluster.NewClient(kube, nil, nil)
	return a
}

// take returns the namespaces asked since the last take, sorted.
func (a *asking) take() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.asked
	a.asked = nil
	slices.Sort(out)
	return out
}

// where is the namespaces asked about, however many kinds each cost.
func (a *asking) where() []string { return slices.Compact(a.take()) }

func (a *asking) set(allow, fail func(*authzv1.ResourceAttributes) bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.allow, a.fail = allow, fail
}

// readsVMsIn allows seeing nss and listing VMs there, and nothing else.
func readsVMsIn(nss ...string) func(*authzv1.ResourceAttributes) bool {
	return func(a *authzv1.ResourceAttributes) bool {
		if a.Verb == "get" && a.Resource == "namespaces" && slices.Contains(nss, a.Name) {
			return true
		}
		return a.Verb == verbList && a.Resource == "virtualmachines" && a.Namespace != "" && slices.Contains(nss, a.Namespace)
	}
}

// seedVisibleAt caches ns as tok's view at the current levels, derived at
// swept, each kind listable everywhere, so a test's reads stay off the
// visibility reviews, and off the snapshot when the server has none.
func seedVisibleAt(s *Server, tok string, ns map[string]bool, swept time.Time) {
	s.views.Put(restfactory.TokenKey(tok), viewMemo{ns: ns, wideReads: allReads, lv: s.visLevels(), built: swept, until: swept.Add(visibleTTL)})
}

func seedVisible(s *Server, tok string, ns map[string]bool) { seedVisibleAt(s, tok, ns, time.Now()) }

// seedVisibleWithReads caches ns as tok's view, each listable there, and
// nothing cluster-wide.
func seedVisibleWithReads(s *Server, tok string, ns map[string]bool) {
	seedVisible(s, tok, ns)
	m, _ := s.views.Get(restfactory.TokenKey(tok))
	m.wideReads, m.reads = 0, map[string]readSet{}
	for n := range ns {
		m.reads[n] = allReads
	}
	s.views.Put(restfactory.TokenKey(tok), m)
}

// ageVerdicts moves tok's cached verdicts on qs back by age, and its view to
// when the first of them stops serving, as deriveView would have stamped it.
func ageVerdicts(s *Server, tok string, age time.Duration, qs ...question) {
	key := restfactory.TokenKey(tok)
	m, held := s.views.Get(key)
	for _, q := range qs {
		k := verdictKey(restfactory.TokenKey(tok), q)
		if v, at, ok := s.ssar.GetAge(k); ok {
			s.ssar.PutAt(k, v, time.Now().Add(-at-age))
			if served := time.Now().Add(s.maxAge(v.unsettled) - at - age); held && served.Before(m.until) {
				m.until = served
			}
		}
	}
	if held {
		s.views.Put(key, m)
	}
}

// wideQuestions is what a sweep of a token seeing every namespace asks.
func wideQuestions() []question {
	qs := []question{refQ(verbGet, ssarNamespace, ""), refQ(verbList, ssarNamespace, "")}
	for _, k := range readKinds {
		qs = append(qs, refQ(verbList, k, ""))
	}
	return qs
}

// nsQuestions is what a sweep asks of a token in ns: the gate and each kind.
func nsQuestions(ns string) []question {
	qs := []question{{Verb: verbGet, Resource: ssarNamespace.resource, Namespace: ns, Name: ns}}
	for _, k := range readKinds {
		qs = append(qs, refQ(verbList, k, ns))
	}
	return qs
}

func projectNS(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"dotvirt.io/project": name}}}
}

// watchedState runs a snapshot over a fake clientset holding objs, whose RBAC
// watches all run: the SA may list every kind, and Groups are absent as on
// any cluster but OpenShift. It returns once the snapshot synced and its
// first lists published. fail makes the ClusterRole watch fail from then on.
func watchedState(t *testing.T, bus *eventbus.Bus, objs ...runtime.Object) (st *clusterstate.State, kube *fake.Clientset, fail func()) {
	t.Helper()
	kube = fake.NewSimpleClientset(objs...)
	var rbac []metav1.APIResource
	for _, r := range []string{"rolebindings", "roles", "clusterrolebindings", "clusterroles"} {
		rbac = append(rbac, metav1.APIResource{Name: r})
	}
	kube.Resources = []*metav1.APIResourceList{{GroupVersion: "rbac.authorization.k8s.io/v1", APIResources: rbac}}
	kube.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ssar := action.(k8stesting.CreateAction).GetObject().(*authzv1.SelfSubjectAccessReview).DeepCopy()
		ssar.Status.Allowed = true
		return true, ssar, nil
	})
	var (
		down atomic.Bool
		mu   sync.Mutex
		w    *watch.FakeWatcher
	)
	kube.PrependWatchReactor("clusterroles", func(k8stesting.Action) (bool, watch.Interface, error) {
		if down.Load() {
			return true, nil, errors.New("watch refused")
		}
		mu.Lock()
		defer mu.Unlock()
		w = watch.NewFake()
		return true, w, nil
	})
	st = clusterstate.New(cluster.NewClient(kube, kvfake.NewSimpleClientset(), nil), "dotvirt.io/project", bus)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st.Run(ctx)
	waitUntil(t, "every RBAC watch running", st.RBACWatched)
	if err := st.WaitForSync(ctx); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the namespace store's first publish", func() bool { return bus.Version(eventbus.NamespaceChanged) > 0 })
	// The gates above pass once a list landed; a reflector opens its watch only
	// after that. An apiserver replays the gap from the list's resourceVersion,
	// the fake drops it, so a write in the gap would never reach a sink. The
	// fake holds its lock across a Watch, so a recorded one has its watcher.
	waitUntil(t, "every watch open", func() bool {
		missing := map[string]bool{"namespaces": true}
		for _, r := range rbac {
			missing[r.Name] = true
		}
		for _, a := range kube.Actions() {
			if a.GetVerb() == "watch" {
				delete(missing, a.GetResource().Resource)
			}
		}
		return len(missing) == 0
	})
	return st, kube, func() {
		down.Store(true)
		mu.Lock()
		defer mu.Unlock()
		w.Stop()
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func visibleOf(t *testing.T, s *Server, tok string, c *cluster.Client) map[string]bool {
	t.Helper()
	got, err := s.visibleFor(context.Background(), auth.Identity{Token: tok, Username: tok}, c)
	if err != nil {
		t.Fatalf("visibleFor(%s): %v", tok, err)
	}
	return got
}

// TestVisibleForCachesPerToken pins the hot-path contract: a token's view
// is reviewed once and every same-level read after that is a pure cache hit -
// a request burst must not fan out into per-request reviews.
func TestVisibleForCachesPerToken(t *testing.T) {
	bus := eventbus.New()
	st, _, _ := watchedState(t, bus, projectNS("tenant-a"), projectNS("tenant-b"))
	s := NewServer(Deps{State: st, Bus: bus, Reader: &fakeDraft{}, Draft: &fakeDraft{}})
	tenant := newAsking(readsVMsIn("tenant-a"))

	if got := visibleOf(t, s, "tok-alice", tenant.c); !maps.Equal(got, map[string]bool{"tenant-a": true}) {
		t.Fatalf("visible = %v, want tenant-a", got)
	}
	if got := tenant.where(); !slices.Equal(got, []string{"", "tenant-a", "tenant-b"}) {
		t.Fatalf("the first read asked %q, want the cluster-wide review and each candidate", got)
	}
	if got := visibleOf(t, s, "tok-alice", tenant.c); !got["tenant-a"] || len(got) != 1 {
		t.Fatalf("cached read = %v, want the same set as the first", got)
	}
	if got := tenant.take(); len(got) != 0 {
		t.Fatalf("a same-level read asked %q", got)
	}
}

// A token that may list VMs and namespaces everywhere sees every namespace,
// project or not, off the SA's own watch: two reviews, no list under its
// token, none per namespace. One that may list VMs everywhere sees every
// project namespace. Listing namespaces never stands in for reading VMs.
func TestVisibleForClusterWideReaders(t *testing.T) {
	bus := eventbus.New()
	st, _, _ := watchedState(t, bus, projectNS("tenant-a"), projectNS("tenant-b"), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}})
	s := NewServer(Deps{State: st, Bus: bus, Reader: &fakeDraft{}, Draft: &fakeDraft{}})

	admin, kube, reviews := ssarCountingFake(func(*authzv1.ResourceAttributes) bool { return true })
	if got := visibleOf(t, s, "tok-admin", admin); !maps.Equal(got, map[string]bool{"tenant-a": true, "tenant-b": true, "default": true}) {
		t.Errorf("admin sees %v, want every namespace", got)
	}
	for _, a := range kube.Actions() {
		if a.GetVerb() == "list" {
			t.Errorf("the admin's visibility listed %s under its token", a.GetResource().Resource)
		}
	}
	if want := wideSweep; *reviews != want {
		t.Errorf("the admin's visibility cost %d reviews, want %d", *reviews, want)
	}

	vms := newAsking(func(a *authzv1.ResourceAttributes) bool {
		return a.Verb == "get" && a.Resource == "namespaces" || a.Resource == "virtualmachines"
	})
	if got := visibleOf(t, s, "tok-vms", vms.c); !maps.Equal(got, map[string]bool{"tenant-a": true, "tenant-b": true}) {
		t.Errorf("a cluster-wide VM reader sees %v, want every project namespace", got)
	}
	lacking := len(readKinds) - 1
	want := slices.Concat(slices.Repeat([]string{""}, wideSweep), slices.Repeat([]string{"tenant-a"}, lacking), slices.Repeat([]string{"tenant-b"}, lacking))
	if got := vms.take(); !slices.Equal(got, want) {
		t.Errorf("a cluster-wide VM reader asked %q, want the wide sweep and each kind it lacks per namespace", got)
	}

	auditor := newAsking(func(a *authzv1.ResourceAttributes) bool { return a.Verb == verbList && a.Resource == "namespaces" })
	if got := visibleOf(t, s, "tok-auditor", auditor.c); len(got) != 0 {
		t.Errorf("a namespace lister without get sees %v, want nothing", got)
	}
}

// Concurrent misses for one token share one sweep: a frame build and the
// pulls its watermarks trigger never review one token's visibility twice. The
// caller that started the sweep leaves on its own deadline, and the sweep runs
// on for the others still waiting on it.
func TestVisibleForSharesOneProbe(t *testing.T) {
	s := scopeServer(eventbus.New())
	kube := fake.NewSimpleClientset()
	gate := make(chan struct{})
	var started atomic.Bool
	var reviews atomic.Int32
	kube.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		reviews.Add(1)
		started.Store(true)
		<-gate
		ssar := action.(k8stesting.CreateAction).GetObject().(*authzv1.SelfSubjectAccessReview).DeepCopy()
		ssar.Status.Allowed = true
		return true, ssar, nil
	})
	c := cluster.NewClient(kube, nil, nil)
	id := auth.Identity{Token: "tok-alice", Username: "alice"}

	leader, leave := context.WithCancel(context.Background())
	left := make(chan error, 1)
	go func() {
		_, err := s.visibleFor(leader, id, c)
		left <- err
	}()
	for !started.Load() {
		time.Sleep(time.Millisecond)
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			if _, err := s.visibleFor(context.Background(), id, c); err != nil {
				t.Errorf("a waiter got %v", err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond) // let the waiters join the sweep in flight
	leave()
	select {
	case err := <-left:
		if err == nil {
			t.Error("the leader left with an answer it never waited for")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the leader could not leave while its sweep ran")
	}
	close(gate)
	wg.Wait()
	if n := int(reviews.Load()); n != wideSweep {
		t.Fatalf("four concurrent misses cost %d reviews, want one sweep's %d", n, wideSweep)
	}
}

// While every RBAC watch runs, a cached view serves for minutes; once
// one fails, the same entry serves only seconds, so a revocation the watches
// cannot report waits no longer than it did before them. A snapshot whose
// watches never ran keeps the short TTL throughout.
func TestVisibleForTTLFollowsTheWatches(t *testing.T) {
	bus := eventbus.New()
	st, _, fail := watchedState(t, bus)
	watched := NewServer(Deps{State: st, Bus: bus, Reader: &fakeDraft{}, Draft: &fakeDraft{}})
	probes := func(s *Server) int {
		t.Helper()
		// The entry a sweep left a minute ago.
		seedVisibleAt(s, "tok-alice", map[string]bool{}, time.Now().Add(-time.Minute))
		c, reviews := ssarCountingClient(func(*authzv1.ResourceAttributes) bool { return true })
		visibleOf(t, s, "tok-alice", c)
		return *reviews
	}

	if n := probes(watched); n != 0 {
		t.Errorf("watched: a minute-old entry cost %d reviews, want 0", n)
	}
	fail()
	waitUntil(t, "the failed watch to count", func() bool { return !st.RBACWatched() })
	if n := probes(watched); n != wideSweep {
		t.Errorf("a watch down: a minute-old entry cost %d reviews, want a sweep's %d", n, wideSweep)
	}
	if n := probes(scopeServer(eventbus.New())); n != wideSweep {
		t.Errorf("never watched: a minute-old entry cost %d reviews, want a sweep's %d", n, wideSweep)
	}
}

// A caller whose deadline has passed posts no review at all, and one whose
// deadline passes mid-review returns on it rather than on the review's bound.
func TestReviewsHonorTheCallerDeadline(t *testing.T) {
	s := scopeServer(eventbus.New())
	c, reviews := ssarCountingClient(func(*authzv1.ResourceAttributes) bool { return true })
	id := auth.Identity{Token: "tok-alice", Username: "alice"}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.can(gone, id, c, refQ(verbList, ssarNodes, "")); err == nil || *reviews != 0 {
		t.Fatalf("an expired caller: err %v after %d reviews, want an error and none", err, *reviews)
	}

	soon, done := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer done()
	begin := time.Now()
	_, err := review(soon, s.ssar, "k", 1, func(ctx context.Context) (ssarVerdict, error) {
		<-ctx.Done()
		return ssarVerdict{}, ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(begin) > time.Second {
		t.Fatalf("a caller with a 20ms deadline returned %v after %s", err, time.Since(begin))
	}
}

// A review that begins right after a grant move may read the old grant from a
// lagging apiserver: its answer serves the short TTL even while every watch
// runs. One that begins once grants settled serves the long TTL.
func TestUnsettledReviewsServeTheShortTTL(t *testing.T) {
	bus := eventbus.New()
	st, _, _ := watchedState(t, bus) // its initial lists are grant moves
	s := NewServer(Deps{State: st, Bus: bus, Reader: &fakeDraft{}, Draft: &fakeDraft{}})
	c, reviews := ssarCountingClient(func(*authzv1.ResourceAttributes) bool { return true })
	visibleOf(t, s, "tok-alice", c)
	key := restfactory.TokenKey("tok-alice")
	m, ok := s.views.Get(key)
	if !ok || m.until.Sub(m.built) > unwatchedTTL+time.Second {
		t.Fatalf("a sweep right after a grant move cached %+v (ok=%v), want it to serve the short TTL", m, ok)
	}
	ageVerdicts(s, "tok-alice", time.Minute, wideQuestions()...)
	if visibleOf(t, s, "tok-alice", c); *reviews != 2*wideSweep {
		t.Fatalf("a minute-old unsettled entry: %d reviews, want a second sweep", *reviews)
	}

	settled := scopeServer(eventbus.New()) // no grant move ever reported
	if settled.unsettled(time.Now(), "") {
		t.Error("a review with no grant move behind it read as unsettled")
	}
	if !s.unsettled(st.LastGrantMove().Add(grantSettle/2), "") || s.unsettled(st.LastGrantMove().Add(grantSettle), "") {
		t.Error("the settle window is not grantSettle long")
	}
}

// The cached set survives only while its levels stand: a cluster-wide grant
// move sweeps again, a move in one namespace re-asks that namespace alone,
// one outside the candidates nothing, and a namespace move that left the
// candidates as they were asks nothing.
func TestVisibleForReasksWhatMoved(t *testing.T) {
	bus := eventbus.New()
	st, _, _ := watchedState(t, bus, projectNS("a"), projectNS("b"), projectNS("c"))
	s := NewServer(Deps{State: st, Bus: bus, Reader: &fakeDraft{}, Draft: &fakeDraft{}})
	tenant := newAsking(readsVMsIn("a", "b"))
	step := func(what string, want ...string) {
		t.Helper()
		if got := visibleOf(t, s, "tok", tenant.c); !maps.Equal(got, map[string]bool{"a": true, "b": true}) {
			t.Fatalf("%s: visible %v", what, got)
		}
		if got := tenant.where(); !slices.Equal(got, want) {
			t.Fatalf("%s: asked %q, want %q", what, got, want)
		}
	}
	step("first read", "", "a", "b", "c")
	step("quiet bus")
	bus.Publish(eventbus.RBACChanged)
	step("a cluster-wide move", "", "a", "b", "c")
	bus.PublishKey(eventbus.RBACChanged, "b")
	step("a move in b", "b")
	bus.PublishKey(eventbus.RBACChanged, "elsewhere")
	step("a move outside the candidates")
	bus.Publish(eventbus.NamespaceChanged)
	step("a namespace move that changed no candidate")
}

// TestVisibleForNoCrossTokenLeak pins tenant isolation inside the cache itself:
// entries are keyed by token, so one caller's visibility can never answer for
// another - and a second caller filling the cache must not evict the first.
func TestVisibleForNoCrossTokenLeak(t *testing.T) {
	bus := eventbus.New()
	st, _, _ := watchedState(t, bus, projectNS("tenant-a"), projectNS("tenant-b"))
	s := NewServer(Deps{State: st, Bus: bus, Reader: &fakeDraft{}, Draft: &fakeDraft{}})
	alice, bob := newAsking(readsVMsIn("tenant-a")), newAsking(readsVMsIn("tenant-b"))

	if got := visibleOf(t, s, "tok-alice", alice.c); !got["tenant-a"] || len(got) != 1 {
		t.Fatalf("alice sees %v, want only tenant-a", got)
	}
	if got := visibleOf(t, s, "tok-bob", bob.c); got["tenant-a"] || !got["tenant-b"] {
		t.Fatalf("bob sees %v: alice's cached set leaked across tokens", got)
	}
	alice.take()
	visibleOf(t, s, "tok-alice", alice.c)
	if got := alice.take(); len(got) != 0 {
		t.Fatalf("alice's re-read asked %q (bob's fill evicted her entry)", got)
	}
}

// TestVisibleForNeverCachesAFailure pins LIVE-1: one failed namespace review
// fails the probe instead of dropping that namespace, and the next read probes
// again instead of serving a cached partial set.
func TestVisibleForNeverCachesAFailure(t *testing.T) {
	fc := newFakeCluster(t)
	fc.readsVMs = tenantReadsTeamA
	var down atomic.Bool
	down.Store(true)
	fc.ssarFail = func(a *authzv1.ResourceAttributes) int {
		if down.Load() && a.Namespace == "orphans-ns" {
			return http.StatusServiceUnavailable
		}
		return 0
	}
	s := fc.server(nil, Config{})
	id := auth.Identity{Token: tenantToken, Username: "tenant"}
	c, err := s.clusterF.For(id.Token)
	if err != nil {
		t.Fatal(err)
	}

	if got, err := s.visibleFor(context.Background(), id, c); err == nil {
		t.Fatalf("a failed review answered %v with nil error", got)
	}
	wantStatus(t, do(s, tenantToken, http.MethodGet, "/api/inventory", ""), http.StatusServiceUnavailable)
	down.Store(false)
	got, err := s.visibleFor(context.Background(), id, c)
	if err != nil || !got["team-a-dev"] || len(got) != 1 {
		t.Fatalf("after recovery: %v, %v; want only team-a-dev (the failure must not be cached)", got, err)
	}
}

// TestOnlyARejectedSessionIs401 pins the session end: an apiserver 401 on
// the caller's review is checked against a fresh TokenReview. A rejected token
// answers 401 (the UI signs out) and signals the session's end to its open
// streams and consoles; a token that still validates - an authenticator blip -
// answers 503 and keeps the session. Neither body names a namespace the probe
// touched.
func TestOnlyARejectedSessionIs401(t *testing.T) {
	fc := newFakeCluster(t)
	// The per-namespace probe fails, so the raw error names the namespace.
	fc.ssarFail = func(a *authzv1.ResourceAttributes) int {
		if a.Namespace != "" {
			return http.StatusUnauthorized
		}
		return 0
	}
	s := fc.server(nil, Config{})
	var alive atomic.Bool
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		tr := action.(k8stesting.CreateAction).GetObject().(*authnv1.TokenReview).DeepCopy()
		tr.Status = authnv1.TokenReviewStatus{Authenticated: alive.Load(), User: authnv1.UserInfo{Username: "tenant"}}
		return true, tr, nil
	})
	s.auth = auth.New(kube, []byte("0123456789abcdef0123456789abcdef"))
	mux := http.NewServeMux()
	s.routes(mux)
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/inventory", nil), tenantToken))
		return rec
	}

	alive.Store(true)
	rec := get()
	wantStatus(t, rec, http.StatusServiceUnavailable)
	if v := s.bus.Version(eventbus.SessionEnded); v != 0 {
		t.Fatalf("a live token signalled %d session ends", v)
	}
	alive.Store(false)
	rec401 := get()
	wantStatus(t, rec401, http.StatusUnauthorized)
	if v := s.bus.Version(eventbus.SessionEnded); v != 1 {
		t.Fatalf("a rejected token signalled %d session ends, want 1", v)
	}
	for _, r := range []*httptest.ResponseRecorder{rec, rec401} {
		if body := r.Body.String(); strings.Contains(body, "team-a-dev") || strings.Contains(body, "orphans-ns") || strings.Contains(body, "of 2") {
			t.Errorf("body names a probed namespace: %q", body)
		}
	}
}

// TestCanCachesVerdicts pins the SSAR cache: one round per (token, question)
// at a given RBAC version - a DENIED verdict is cached exactly like an allowed
// one (else every unauthorized poll re-posts an SSAR), a version bump re-asks,
// and a different token never reuses another's verdict.
func TestCanCachesVerdicts(t *testing.T) {
	bus := eventbus.New()
	s := scopeServer(bus)
	c, reviews := ssarCountingClient(func(a *authzv1.ResourceAttributes) bool {
		return a.Verb == "create" && a.Resource == ssarCUDN.resource
	})
	ctx := context.Background()
	id := auth.Identity{Token: "tok-admin", Username: "admin"}
	can := func(id auth.Identity, ref ssarRef) bool {
		t.Helper()
		ok, err := s.can(ctx, id, c, refQ(verbCreate, ref, ""))
		if err != nil {
			t.Fatalf("can: %v", err)
		}
		return ok
	}

	for range 2 {
		if !can(id, ssarCUDN) {
			t.Fatal("allowed ref should read true")
		}
	}
	if *reviews != 1 {
		t.Fatalf("allowed verdict: %d rounds, want 1", *reviews)
	}
	for range 2 {
		if can(id, ssarMachineCfg) {
			t.Fatal("denied ref should read false")
		}
	}
	if *reviews != 2 {
		t.Fatalf("denied verdict must be cached too: %d rounds, want 2", *reviews)
	}
	bus.Publish(eventbus.RBACChanged)
	if !can(id, ssarCUDN) {
		t.Fatal("re-asked ref should still read true")
	}
	if *reviews != 3 {
		t.Fatalf("after RBACChanged: %d rounds, want 3 (verdict re-asked)", *reviews)
	}
	can(auth.Identity{Token: "tok-other", Username: "other"}, ssarCUDN)
	if *reviews != 4 {
		t.Fatalf("distinct token reused a cached verdict: %d rounds, want 4", *reviews)
	}
}

// TestCanKeyIsolation pins that every part of the question keys the cache:
// verb, group, resource, subresource, namespace and name each make a distinct
// review, so a crafted ref can neither read nor overwrite another question's
// verdict.
func TestCanKeyIsolation(t *testing.T) {
	s := scopeServer(eventbus.New())
	c, reviews := ssarCountingClient(func(a *authzv1.ResourceAttributes) bool {
		return a.Verb == "list" && a.Resource == "nodes" && a.Group == "" && a.Namespace == "" && a.Subresource == "" && a.Name == ""
	})
	ctx := context.Background()
	id := auth.Identity{Token: "tok-net", Username: "net"}

	for i, q := range []struct {
		q    question
		want bool
	}{
		{question{Verb: verbList, Resource: "nodes"}, true},
		{question{Verb: verbList, Resource: "nodes"}, true},
		{question{Verb: verbCreate, Resource: "nodes"}, false},
		{question{Verb: verbList, Group: "read", Resource: "nodes"}, false},
		{question{Verb: verbList, Resource: "nodes", Namespace: "ns"}, false},
		{question{Verb: verbList, Resource: "nodes", Subresource: "proxy"}, false},
		{question{Verb: verbList, Resource: "nodes", Name: "w1"}, false},
		{question{Verb: verbList, Resource: "nodes"}, true},
	} {
		if ok, err := s.can(ctx, id, c, q.q); ok != q.want || err != nil {
			t.Fatalf("question %d = %v, %v; want %v", i, ok, err, q.want)
		}
	}
	if *reviews != 6 {
		t.Fatalf("reviews = %d, want 6 (one per distinct question)", *reviews)
	}
}

// TestCanNeverCachesAFailure pins API-2: a failed review is the caller's
// error, never a cached denial - the gate answers 503, not 403, and the next
// request after recovery is allowed instead of denied for the TTL.
func TestCanNeverCachesAFailure(t *testing.T) {
	fc := newFakeCluster(t)
	var down atomic.Bool
	down.Store(true)
	fc.ssarFail = func(*authzv1.ResourceAttributes) int {
		if down.Load() {
			return http.StatusServiceUnavailable
		}
		return 0
	}
	s := fc.server(&planeDraft{}, Config{PlatformRepo: platformRepo})

	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/drs", `{}`), http.StatusServiceUnavailable)
	down.Store(false)
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/drs", `{}`), http.StatusOK)
}

// platformFactory builds a real per-token cluster.Factory whose kubeconfig points
// at a fake apiserver that answers only SSARs, allowing exactly the admin token.
func platformFactory(t *testing.T) *cluster.Factory {
	t.Helper()
	return ssarFactory(t, func(r *http.Request, _ *authzv1.SelfSubjectAccessReview) bool {
		return r.Header.Get("Authorization") == "Bearer admin-token"
	})
}

// ssarFactory builds a real per-token cluster.Factory over a fake apiserver that
// answers only SSARs, with allow deciding each review from the request and its
// attributes. Client construction never dials, so everything but the SSAR stays
// offline.
func ssarFactory(t *testing.T, allow func(*http.Request, *authzv1.SelfSubjectAccessReview) bool) *cluster.Factory {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
			http.NotFound(w, r)
			return
		}
		// client-go posts protobuf; the universal deserializer reads either wire form.
		body, _ := io.ReadAll(r.Body)
		obj, _, err := scheme.Codecs.UniversalDeserializer().Decode(body, nil, &authzv1.SelfSubjectAccessReview{})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp := authzv1.SelfSubjectAccessReview{
			TypeMeta: metav1.TypeMeta{Kind: "SelfSubjectAccessReview", APIVersion: "authorization.k8s.io/v1"},
			Status:   authzv1.SubjectAccessReviewStatus{Allowed: allow(r, obj.(*authzv1.SelfSubjectAccessReview))},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	cfg := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: %s
contexts:
- name: test
  context:
    cluster: test
    user: test
current-context: test
users:
- name: test
  user:
    token: unused
`, srv.URL)
	if err := os.WriteFile(kubeconfig, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := cluster.NewFactory(kubeconfig)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	return f
}

// TestPlatformScope pins the platform-tier gate end to end: no configured repo
// fails closed as 503 before any SSAR, a token the cluster denies gets 403, and
// an allowed token gets the synthetic platform project (config-only - never a
// discovered namespace).
func TestPlatformScope(t *testing.T) {
	f := platformFactory(t)
	// draft only needs to be non-nil; platformScope never calls it.
	newScopeServer := func(repo string) *Server {
		return NewServer(Deps{ClusterFactory: f, Reader: &fakeDraft{}, Draft: &fakeDraft{}, Config: Config{PlatformRepo: repo}})
	}
	request := func(token string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/networks", nil)
		return r.WithContext(auth.NewContext(r.Context(), auth.Identity{Token: token, Username: "u"}))
	}

	t.Run("no platform repo fails closed", func(t *testing.T) {
		s := newScopeServer("")
		rec := httptest.NewRecorder()
		if _, ok := s.platformScope(rec, request("admin-token"), ssarCUDN); ok {
			t.Fatal("scope resolved without a platform repo")
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
	})

	t.Run("denied SSAR is forbidden", func(t *testing.T) {
		s := newScopeServer("https://forge/platform.git")
		rec := httptest.NewRecorder()
		if _, ok := s.platformScope(rec, request("tenant-token"), ssarCUDN); ok {
			t.Fatal("scope resolved for a token the cluster denies")
		}
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("allowed token gets the synthetic project", func(t *testing.T) {
		s := newScopeServer("https://forge/platform.git")
		rec := httptest.NewRecorder()
		sc, ok := s.platformScope(rec, request("admin-token"), ssarCUDN)
		if !ok {
			t.Fatalf("platformScope failed: %d %s", rec.Code, rec.Body.String())
		}
		if sc.proj.Name != platformProjectName || sc.proj.Repo != "https://forge/platform.git" {
			t.Fatalf("proj = %+v, want the synthetic platform project", sc.proj)
		}
		if sc.id.Token != "admin-token" || sc.cluster == nil {
			t.Fatal("scope must carry the caller's identity and cluster client")
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("success wrote status %d", rec.Code)
		}
	})
}

// fakeModules answers the discovery the modules ask, KubeVirt or none.
type fakeModules struct{ vms bool }

func (f fakeModules) ServesAPIResource(gvr schema.GroupVersionResource) (bool, error) {
	return f.vms && gvr.Resource == "virtualmachines", nil
}

func (fakeModules) HasAny(context.Context, schema.GroupVersionResource) (bool, error) {
	return false, nil
}

func knownModules(t *testing.T, vms bool) *modules.Snapshot {
	t.Helper()
	snap := modules.New(fakeModules{vms})
	snap.Run(t.Context())
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if _, ok := snap.Get(); ok {
			return snap
		}
	}
	t.Fatal("the modules never answered")
	return nil
}

// A namespace admin's grants, on a cluster where KubeVirt's aggregated roles do
// or do not exist: the namespace, NetworkPolicies, VMs only with KubeVirt.
func namespaceAdmin(withVMs bool) func(string, *authzv1.ResourceAttributes) bool {
	return func(token string, a *authzv1.ResourceAttributes) bool {
		if token != tenantToken || a.Namespace != "team-a-dev" {
			return false
		}
		return a.Resource == "namespaces" || a.Resource == "pods" || a.Resource == "networkpolicies" || (withVMs && a.Resource == "virtualmachines")
	}
}

// A log shipper or a monitoring account: pods everywhere, nothing else.
func podsOnly(token string, a *authzv1.ResourceAttributes) bool {
	return token == tenantToken && a.Resource == "pods"
}

func tenantView(t *testing.T, s *Server) (map[string]bool, map[string]bool) {
	t.Helper()
	id := auth.Identity{Token: tenantToken, Username: "tenant"}
	c, err := s.clusterF.For(id.Token)
	if err != nil {
		t.Fatal(err)
	}
	visible, err := s.visibleFor(context.Background(), id, c)
	if err != nil {
		t.Fatal(err)
	}
	return visible, s.authorNamespaces(context.Background(), id, c, []string{"team-a-dev"})
}

func TestANamespaceAdminSeesItsNamespaceWithoutKubeVirt(t *testing.T) {
	fc := newFakeCluster(t)
	fc.ssar = namespaceAdmin(false)
	s := fc.server(nil, Config{})
	s.modules = knownModules(t, false)
	visible, author := tenantView(t, s)
	if !visible["team-a-dev"] || len(visible) != 1 {
		t.Errorf("visible = %v, want team-a-dev alone", visible)
	}
	if !author["team-a-dev"] {
		t.Errorf("author = %v, want true on NetworkPolicies", author)
	}
}

// Seeing a namespace opens its project's repo and network data.
func TestRightsOnPodsAloneOpenNothing(t *testing.T) {
	for name, vms := range map[string]bool{"with KubeVirt": true, "without": false} {
		t.Run(name, func(t *testing.T) {
			fc := newFakeCluster(t)
			fc.ssar = podsOnly
			s := fc.server(nil, Config{})
			s.modules = knownModules(t, vms)
			if visible, author := tenantView(t, s); len(visible) != 0 || author["team-a-dev"] {
				t.Errorf("visible = %v, author = %v, want neither", visible, author)
			}
		})
	}
}

// A namespace admin of a cluster with KubeVirt but none of its roles sees the
// namespace and authors no VMs in it.
func TestWithKubeVirtAuthoringStillAsksAboutVMs(t *testing.T) {
	fc := newFakeCluster(t)
	fc.ssar = namespaceAdmin(false)
	s := fc.server(nil, Config{})
	s.modules = knownModules(t, true)
	if visible, author := tenantView(t, s); !visible["team-a-dev"] || author["team-a-dev"] {
		t.Errorf("visible = %v, author = %v, want the namespace and no authoring", visible, author)
	}
}

// Until discovery answers, authoring keeps the VM question.
func TestUnknownModulesKeepTheVMQuestion(t *testing.T) {
	fc := newFakeCluster(t)
	fc.ssar = namespaceAdmin(false)
	s := fc.server(nil, Config{})
	s.modules = modules.New(fakeModules{})
	if visible, author := tenantView(t, s); !visible["team-a-dev"] || author["team-a-dev"] {
		t.Errorf("visible = %v, author = %v, want the namespace and no authoring", visible, author)
	}
}

// resourceNames can open one namespace through a ClusterRoleBinding, which the
// RoleBinding index cannot see: the gate asks such a caller everywhere.
func TestTheGateAsksEverywhereForAClusterBoundCaller(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.New()
	st, kube, _ := watchedState(t, bus, projectNS("mine"), projectNS("theirs"), userBinding("mine", "owners", "alice"))
	waitUntil(t, "the RBAC watches", func() bool { return !st.ClusterBound("alice", nil) })
	s := scopeServer(bus)
	s.state, s.indexBindings = st, true
	all := []string{"mine", "theirs"}
	everyone := []string{"system:authenticated"}
	alice := auth.Identity{Username: "alice", Groups: everyone}
	if got := s.gateOnly(alice, all); !slices.Equal(got, []string{"mine"}) {
		t.Errorf("before: asked %v, want the bound namespace", got)
	}
	// In watch order: once the second lands, the first was seen too.
	createCRB(t, ctx, kube, "1-basic", rbacv1.Subject{Kind: rbacv1.GroupKind, Name: "system:authenticated"})
	createCRB(t, ctx, kube, "2-one-namespace", rbacv1.Subject{Kind: rbacv1.UserKind, Name: "alice"})
	waitUntil(t, "the ClusterRoleBinding", func() bool { return st.ClusterBound("alice", everyone) })
	if got := s.gateOnly(alice, all); !slices.Equal(got, all) {
		t.Errorf("named by a ClusterRoleBinding: asked %v, want everywhere", got)
	}
	if got := s.gateOnly(auth.Identity{Username: "bob", Groups: everyone}, all); len(got) != 0 {
		t.Errorf("a binding to system:authenticated stopped the narrowing for bob: %v", got)
	}
	// A list names no object: the reads stay narrowed for alice.
	if got := s.boundOnly(alice, all); !slices.Equal(got, []string{"mine"}) {
		t.Errorf("reads asked %v, want the bound namespace", got)
	}
}

func createCRB(t *testing.T, ctx context.Context, kube *fake.Clientset, name string, sub rbacv1.Subject) {
	t.Helper()
	_, err := kube.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "view"},
		Subjects:   []rbacv1.Subject{sub},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
}

// wideSweep is what a token seeing every namespace costs: the two cluster-wide
// visibility reviews and one per kind a view narrows by.
var wideSweep = 2 + len(readKinds)

// Seeing a namespace is not seeing all it holds: each row asks its own kind.
func TestAViewNarrowsByKind(t *testing.T) {
	vmsOnly := callerView{memo: viewMemo{reads: map[string]readSet{"a": 1}}}
	if !vmsOnly.lists(ssarVM, "a") || vmsOnly.lists(ssarNetpol, "a") || vmsOnly.lists(ssarVM, "b") {
		t.Errorf("a namespace reader's rights: %+v", vmsOnly.memo.reads)
	}
	if vmsOnly.listsBacking("no-such-kind", "a") {
		t.Error("an unknown backing was shown")
	}
	wide := callerView{memo: viewMemo{wideReads: allReads}}
	if !wide.lists(ssarNetpol, "anywhere") || !wide.listsBacking(model.KindUDN.Kind, "anywhere") {
		t.Error("a cluster-wide reader was narrowed")
	}
}

// A VM page opens only for who may list VMs there.
func TestVMRoutesNeedVMRights(t *testing.T) {
	fc := newFakeCluster(t)
	fc.readsVMs = tenantReadsTeamA
	s := fc.server(nil, Config{})
	now := time.Now()
	s.views.Put(restfactory.TokenKey(tenantToken), viewMemo{
		ns: map[string]bool{"team-a-dev": true}, lv: s.visLevels(), built: now, until: now.Add(visibleTTL),
		reads: map[string]readSet{"team-a-dev": 0},
	})
	wantStatus(t, do(s, tenantToken, http.MethodGet, "/api/vms/team-a-dev/web/manifest", ""), http.StatusNotFound)
}

// A user costs reviews for where they belong, not every namespace on the cluster.
func TestASweepAsksOnlyWhereABindingNamesTheCaller(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.New()
	st, _, _ := watchedState(t, bus, projectNS("mine"), projectNS("theirs"), userBinding("mine", "owners", "alice"))
	waitUntil(t, "the RBAC watches", func() bool {
		_, ok := st.BoundNamespaces("alice", nil)
		return ok
	})
	waitUntil(t, "the namespaces", func() bool { return len(st.Namespaces()) == 2 })
	s := scopeServer(bus)
	s.state, s.indexBindings = st, true
	token := newAsking(func(a *authzv1.ResourceAttributes) bool { return a.Namespace == "mine" })
	v, err := s.viewFor(ctx, auth.Identity{Token: "t", Username: "alice"}, token.c)
	if err != nil {
		t.Fatal(err)
	}
	if !v.names["mine"] || v.names["theirs"] {
		t.Errorf("visible = %v", v.names)
	}
	if got := token.where(); !slices.Equal(got, []string{"", "mine"}) {
		t.Errorf("asked %q, want the cluster-wide review and the bound namespace", got)
	}

	// Wide but lacking a kind: a kind it lacks everywhere can only be held
	// where a RoleBinding names it.
	reader := newAsking(func(a *authzv1.ResourceAttributes) bool {
		return a.Verb == "get" && a.Resource == "namespaces" || a.Resource == "virtualmachines"
	})
	if _, err := s.viewFor(ctx, auth.Identity{Token: "w", Username: "alice"}, reader.c); err != nil {
		t.Fatal(err)
	}
	want := slices.Concat(slices.Repeat([]string{""}, wideSweep), slices.Repeat([]string{"mine"}, len(readKinds)-1))
	if got := reader.take(); !slices.Equal(got, want) {
		t.Errorf("a wide reader asked %q, want the wide sweep and the bound namespace", got)
	}
}

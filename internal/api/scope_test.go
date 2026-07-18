package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/eventbus"
)

// scopeServer builds a Server with only what the scope caches read: the version
// bus and an unsynced snapshot (empty topology is fine — the fake user client's
// cluster-wide namespace list is the visibility source, not the candidates).
func scopeServer(bus *eventbus.Bus) *Server {
	sa := cluster.NewClient(fake.NewSimpleClientset(), nil, nil)
	return NewServer(Deps{State: clusterstate.New(sa, "dotvirt.io/project", bus), Bus: bus})
}

// listCountingClient is a user-identity client over a fake clientset seeded with
// nss, counting namespace LISTs — "cached" is pinned as "no second round". The
// modeled token is admin-like: the cluster-wide VM read SSAR that qualifies the
// namespace-list fast path is allowed.
func listCountingClient(nss ...string) (*cluster.Client, *int) {
	objs := make([]runtime.Object, 0, len(nss))
	for _, ns := range nss {
		objs = append(objs, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	}
	kube := fake.NewSimpleClientset(objs...)
	lists := new(int)
	kube.PrependReactor("list", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		*lists++
		return false, nil, nil // count only; the tracker still answers
	})
	kube.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ssar := action.(k8stesting.CreateAction).GetObject().(*authzv1.SelfSubjectAccessReview).DeepCopy()
		ssar.Status.Allowed = true
		return true, ssar, nil
	})
	return cluster.NewClient(kube, nil, nil), lists
}

// ssarCountingClient answers SelfSubjectAccessReviews via allow and counts them.
func ssarCountingClient(allow func(*authzv1.ResourceAttributes) bool) (*cluster.Client, *int) {
	kube := fake.NewSimpleClientset()
	reviews := new(int)
	kube.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		*reviews++
		ssar := action.(k8stesting.CreateAction).GetObject().(*authzv1.SelfSubjectAccessReview).DeepCopy()
		ssar.Status.Allowed = allow(ssar.Spec.ResourceAttributes)
		return true, ssar, nil
	})
	return cluster.NewClient(kube, nil, nil), reviews
}

// TestVisibleForCachesPerToken pins the hot-path contract: a token's visible set
// is computed against the cluster once and every same-version read after that is
// a pure cache hit — a request burst must not fan out into per-request LISTs.
func TestVisibleForCachesPerToken(t *testing.T) {
	s := scopeServer(eventbus.New())
	c, lists := listCountingClient("tenant-a", "tenant-b")
	id := auth.Identity{Token: "tok-alice", Username: "alice"}

	first, err := s.visibleFor(context.Background(), id, c)
	if err != nil {
		t.Fatalf("visibleFor: %v", err)
	}
	if !first["tenant-a"] || !first["tenant-b"] || len(first) != 2 {
		t.Fatalf("visible = %v, want tenant-a and tenant-b", first)
	}
	second, err := s.visibleFor(context.Background(), id, c)
	if err != nil {
		t.Fatalf("visibleFor (cached): %v", err)
	}
	if *lists != 1 {
		t.Fatalf("two same-version reads cost %d cluster rounds; want 1", *lists)
	}
	if len(second) != len(first) || !second["tenant-a"] || !second["tenant-b"] {
		t.Fatalf("cached read = %v, want the same set as the first", second)
	}
}

// TestVisibleForRBACVersionInvalidates pins the invalidation lever: the cached
// set survives only while bus.Version(RBACChanged, NamespaceChanged) is
// unchanged — either kind moving (a RoleBinding OR a project namespace) must
// force a fresh cluster round, because both can change what a token may see.
func TestVisibleForRBACVersionInvalidates(t *testing.T) {
	bus := eventbus.New()
	s := scopeServer(bus)
	c, lists := listCountingClient("tenant-a")
	id := auth.Identity{Token: "tok-alice", Username: "alice"}
	read := func() {
		if _, err := s.visibleFor(context.Background(), id, c); err != nil {
			t.Fatalf("visibleFor: %v", err)
		}
	}

	read()
	read()
	if *lists != 1 {
		t.Fatalf("quiet bus: %d rounds, want 1", *lists)
	}
	bus.Publish(eventbus.RBACChanged)
	read()
	if *lists != 2 {
		t.Fatalf("after RBACChanged: %d rounds, want 2 (stale entry must not be served)", *lists)
	}
	bus.Publish(eventbus.NamespaceChanged)
	read()
	if *lists != 3 {
		t.Fatalf("after NamespaceChanged: %d rounds, want 3 (namespace moves are RBAC too)", *lists)
	}
}

// TestVisibleForNoCrossTokenLeak pins tenant isolation inside the cache itself:
// entries are keyed by token, so one caller's visibility can never answer for
// another — and a second caller filling the cache must not evict the first.
func TestVisibleForNoCrossTokenLeak(t *testing.T) {
	s := scopeServer(eventbus.New())
	cAlice, aliceLists := listCountingClient("tenant-a")
	cBob, _ := listCountingClient("tenant-b")
	alice := auth.Identity{Token: "tok-alice", Username: "alice"}
	bob := auth.Identity{Token: "tok-bob", Username: "bob"}

	got, err := s.visibleFor(context.Background(), alice, cAlice)
	if err != nil {
		t.Fatalf("visibleFor(alice): %v", err)
	}
	if !got["tenant-a"] || len(got) != 1 {
		t.Fatalf("alice sees %v, want only tenant-a", got)
	}
	got, err = s.visibleFor(context.Background(), bob, cBob)
	if err != nil {
		t.Fatalf("visibleFor(bob): %v", err)
	}
	if got["tenant-a"] || !got["tenant-b"] {
		t.Fatalf("bob sees %v: alice's cached set leaked across tokens", got)
	}
	// Bob's fill must not have displaced alice's entry.
	if _, err := s.visibleFor(context.Background(), alice, cAlice); err != nil {
		t.Fatalf("visibleFor(alice, again): %v", err)
	}
	if *aliceLists != 1 {
		t.Fatalf("alice re-read cost %d rounds, want 1 (bob's fill evicted her entry)", *aliceLists)
	}
}

// TestCanCreateCachedVerdicts pins the SSAR cache: one round per (token, ref) at
// a given RBAC version — a DENIED verdict is cached exactly like an allowed one
// (else every unauthorized poll re-posts an SSAR), a version bump re-asks, and a
// different token never reuses another's verdict.
func TestCanCreateCachedVerdicts(t *testing.T) {
	bus := eventbus.New()
	s := scopeServer(bus)
	c, reviews := ssarCountingClient(func(a *authzv1.ResourceAttributes) bool {
		return a.Verb == "create" && a.Resource == ssarCUDN.resource
	})
	ctx := context.Background()
	id := auth.Identity{Token: "tok-admin", Username: "admin"}

	first, second := s.canCreateCached(ctx, id, c, ssarCUDN), s.canCreateCached(ctx, id, c, ssarCUDN)
	if !first || !second {
		t.Fatal("allowed ref should read true")
	}
	if *reviews != 1 {
		t.Fatalf("allowed verdict: %d rounds, want 1", *reviews)
	}
	first, second = s.canCreateCached(ctx, id, c, ssarMachineCfg), s.canCreateCached(ctx, id, c, ssarMachineCfg)
	if first || second {
		t.Fatal("denied ref should read false")
	}
	if *reviews != 2 {
		t.Fatalf("denied verdict must be cached too: %d rounds, want 2", *reviews)
	}
	bus.Publish(eventbus.RBACChanged)
	if !s.canCreateCached(ctx, id, c, ssarCUDN) {
		t.Fatal("re-asked ref should still read true")
	}
	if *reviews != 3 {
		t.Fatalf("after RBACChanged: %d rounds, want 3 (verdict re-asked)", *reviews)
	}
	other := auth.Identity{Token: "tok-other", Username: "other"}
	s.canCreateCached(ctx, other, c, ssarCUDN)
	if *reviews != 4 {
		t.Fatalf("distinct token reused a cached verdict: %d rounds, want 4", *reviews)
	}
}

// TestCanReadNodesCachedKeyIsolation pins two things: the node-read signal is
// cached like the create verdicts, and its cache key lives outside the create
// tuple namespace — a crafted create ref spelling "read"/"nodes" must neither
// read nor overwrite the node-read verdict.
func TestCanReadNodesCachedKeyIsolation(t *testing.T) {
	s := scopeServer(eventbus.New())
	c, reviews := ssarCountingClient(func(a *authzv1.ResourceAttributes) bool {
		return a.Verb == "list" && a.Resource == "nodes"
	})
	ctx := context.Background()
	id := auth.Identity{Token: "tok-net", Username: "net"}

	first, second := s.canReadNodesCached(ctx, id, c), s.canReadNodesCached(ctx, id, c)
	if !first || !second {
		t.Fatal("node read should be allowed")
	}
	if *reviews != 1 {
		t.Fatalf("node-read verdict: %d rounds, want 1", *reviews)
	}
	// The nearest possible create-tuple collision with the read key.
	near := ssarRef{group: "read", resource: "nodes"}
	if s.canCreateCached(ctx, id, c, near) {
		t.Fatal("create nodes must not inherit the cached read-nodes allow")
	}
	if *reviews != 2 {
		t.Fatalf("crafted create ref answered from the read entry: %d rounds, want 2", *reviews)
	}
	if !s.canReadNodesCached(ctx, id, c) || *reviews != 2 {
		t.Fatalf("read-nodes verdict clobbered by the create ref: allowed=%v rounds=%d", false, *reviews)
	}
}

// platformFactory builds a real per-token cluster.Factory whose kubeconfig points
// at a fake apiserver that answers only SSARs, allowing exactly the admin token.
// Client construction never dials, so everything but the SSAR stays offline.
func platformFactory(t *testing.T) *cluster.Factory {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
			http.NotFound(w, r)
			return
		}
		resp := authzv1.SelfSubjectAccessReview{
			TypeMeta: metav1.TypeMeta{Kind: "SelfSubjectAccessReview", APIVersion: "authorization.k8s.io/v1"},
			Status:   authzv1.SubjectAccessReviewStatus{Allowed: r.Header.Get("Authorization") == "Bearer admin-token"},
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
// an allowed token gets the synthetic platform project (config-only — never a
// discovered namespace).
func TestPlatformScope(t *testing.T) {
	f := platformFactory(t)
	// draft only needs to be non-nil; platformScope never calls it.
	newScopeServer := func(repo string) *Server {
		return NewServer(Deps{ClusterFactory: f, Draft: &fakeDraft{}, Config: Config{PlatformRepo: repo}})
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

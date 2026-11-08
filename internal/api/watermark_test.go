package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	kvfake "kubevirt.io/client-go/kubevirt/fake"

	"github.com/epheo/dotvirt/internal/argo"
	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netstate"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/tasks"
)

const tenantBToken = "tenant-b-token"

func nad(ns, name, config string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
		"metadata": map[string]any{"namespace": ns, "name": name},
		"spec":     map[string]any{"config": config},
	}}
}

// liveCatalog runs a netstate snapshot over a fake dynamic client serving NADs,
// returning the client to write through once the watch is established.
func liveCatalog(t *testing.T, bus *eventbus.Bus, objs ...*unstructured.Unstructured) (*netstate.Snapshot, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	gvr := cluster.GVR(model.KindNAD)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "NetworkAttachmentDefinitionList"})
	// Created through the resource, not seeded: seeding would guess the
	// resource name from the kind and miss the hyphenated plural.
	for _, obj := range objs {
		if _, err := dyn.Resource(gvr).Namespace(obj.GetNamespace()).Create(context.Background(), obj, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	watching := make(chan struct{})
	dyn.PrependWatchReactor(gvr.Resource, func(k8stesting.Action) (bool, watch.Interface, error) {
		close(watching)
		return false, nil, nil
	})
	kube := fake.NewSimpleClientset()
	kube.Resources = []*metav1.APIResourceList{{
		GroupVersion: gvr.GroupVersion().String(),
		APIResources: []metav1.APIResource{{Name: gvr.Resource, Namespaced: true, Kind: model.KindNAD.Kind}},
	}}
	ns := netstate.New(cluster.NewClient(kube, kvfake.NewSimpleClientset(), dyn), bus)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ns.Run(ctx)
	select {
	case <-watching:
	case <-time.After(10 * time.Second):
		t.Fatal("the catalog never watched")
	}
	return ns, dyn
}

// One tenant's frame never moves with another tenant's act: neither a task
// recorded in, nor a network changed in, a namespace it cannot see. Its own
// acts move its own watermarks.
func TestFrameWatermarksScopedToTheCaller(t *testing.T) {
	fc := newFakeCluster(t)
	tenant := func(ns, proj string) corev1.Namespace {
		return corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: map[string]string{"dotvirt.io/project": proj}}}
	}
	fc.namespaces = []corev1.Namespace{tenant("team-a-dev", "team-a"), tenant("team-b-dev", "team-b")}
	fc.readsVMs = func(token, ns string) bool {
		return token == tenantToken && ns == "team-a-dev" || token == tenantBToken && ns == "team-b-dev"
	}
	s := fc.server(nil, Config{})
	s.tasks = tasks.New(s.bus)
	var dyn *dynamicfake.FakeDynamicClient
	s.netstate, dyn = liveCatalog(t, s.bus, nad("team-a-dev", "net", "a1"), nad("team-b-dev", "net", "b1"))
	nads := dyn.Resource(cluster.GVR(model.KindNAD))

	frame := func(token string) (model.Inventory, string) {
		t.Helper()
		inv, err := s.InventoryForIdentity(context.Background(), auth.Identity{Token: token, Username: token})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(inv)
		return inv, string(b)
	}
	// eventually waits for the catalog's watch to deliver a write.
	eventually := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	a0, aJSON := frame(tenantToken)
	b0, _ := frame(tenantBToken)

	s.tasks.RecordOp(tasks.Op{Verb: "Restart", Namespace: "team-b-dev", Name: "db", By: "bob", OK: true, At: time.Now()})
	if _, got := frame(tenantToken); got != aJSON {
		t.Errorf("another tenant's task changed this frame:\n%s\n%s", aJSON, got)
	}
	b1, _ := frame(tenantBToken)
	if b1.TasksVersion == b0.TasksVersion {
		t.Error("a task in the caller's namespace must move its tasks watermark")
	}

	if _, err := nads.Namespace("team-b-dev").Update(context.Background(), nad("team-b-dev", "net", "b2"), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually("the other tenant's network change", func() bool {
		b, _ := frame(tenantBToken)
		return b.NetworksVersion != b1.NetworksVersion
	})
	if _, got := frame(tenantToken); got != aJSON {
		t.Errorf("another tenant's network change changed this frame:\n%s\n%s", aJSON, got)
	}

	s.tasks.RecordOp(tasks.Op{Verb: "Restart", Namespace: "team-a-dev", Name: "web", By: "alice", OK: true, At: time.Now()})
	if a1, _ := frame(tenantToken); a1.TasksVersion == a0.TasksVersion {
		t.Error("an own task must move the tasks watermark")
	}
	if _, err := nads.Namespace("team-a-dev").Update(context.Background(), nad("team-a-dev", "net", "a2"), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually("an own network change", func() bool {
		a, _ := frame(tenantToken)
		return a.NetworksVersion != a0.NetworksVersion
	})
}

// The tasks watermark follows the feed the caller pulls: merges in its own
// projects' repos, node ops only with node read.
func TestTasksWatermarkFollowsTheScopedFeed(t *testing.T) {
	s := &Server{tasks: tasks.New(nil)}
	mine := []project.ProjectInfo{{Name: "team-a", Namespaces: []string{"team-a-dev"}, Repo: "https://forge/team-a.git"}}
	reader, tenant := s.tasksWatermark(mine, true), s.tasksWatermark(mine, false)
	check := func(what string, tenantMoves, readerMoves bool) {
		t.Helper()
		gotT, gotR := s.tasksWatermark(mine, false), s.tasksWatermark(mine, true)
		if (gotT != tenant) != tenantMoves || (gotR != reader) != readerMoves {
			t.Errorf("%s: tenant moved %v, node reader moved %v; want %v, %v", what, gotT != tenant, gotR != reader, tenantMoves, readerMoves)
		}
		tenant, reader = gotT, gotR
		if gotR > wireMask || gotT > wireMask {
			t.Errorf("%s: a watermark past 53 bits", what)
		}
	}
	now := time.Now()
	s.tasks.RecordMerge(tasks.Merge{RepoURL: "https://forge/team-b", Number: 1, Title: "theirs", At: now})
	check("another project's merge", false, false)
	s.tasks.RecordMerge(tasks.Merge{RepoURL: "https://forge/team-a", Number: 2, Title: "ours", At: now})
	check("an own merge", true, true)
	s.tasks.RecordMerge(tasks.Merge{RepoURL: "https://forge/team-a", Number: 2, Title: "ours, retitled", At: now})
	check("an own merge edited", true, true)
	s.tasks.RecordOp(tasks.Op{Verb: "Cordon", Name: "worker-1", At: now})
	check("a node op", false, true)
}

// BenchmarkFrameWatermarks is the api share of one frame's watermarks (the
// catalog's own share is netstate's BenchmarkWatermark): a feed of 100 ops
// over 50 namespaces and 20 merges, for a tenant of 5 namespaces and an admin
// of all 50 who reads nodes.
func BenchmarkFrameWatermarks(b *testing.B) {
	s := &Server{tasks: tasks.New(nil), drift: argo.NewSnapshot(nil, nil)}
	now := time.Now()
	for i := range 100 {
		s.tasks.RecordOp(tasks.Op{Verb: "Restart", Namespace: fmt.Sprintf("ns-%d", i%50), Name: fmt.Sprintf("vm-%d", i), By: "alice", OK: true, At: now})
	}
	for i := range 20 {
		s.tasks.RecordMerge(tasks.Merge{RepoURL: fmt.Sprintf("https://forge/p-%d", i), Number: i, Title: "change", By: "alice", At: now})
	}
	scope := func(n int) ([]project.ProjectInfo, map[string]bool) {
		var projects []project.ProjectInfo
		visible := map[string]bool{}
		for i := range n {
			ns := fmt.Sprintf("ns-%d", i)
			projects = append(projects, project.ProjectInfo{Name: ns, Namespaces: []string{ns}, Repo: fmt.Sprintf("https://forge/p-%d.git", i)})
			visible[ns] = true
		}
		return projects, visible
	}
	for _, c := range []struct {
		name  string
		n     int
		nodes bool
	}{{"tenant", 5, false}, {"admin", 50, true}} {
		b.Run(c.name, func(b *testing.B) {
			projects, visible := scope(c.n)
			b.ReportAllocs()
			g := netGrants{nodes: c.nodes, create: map[ssarRef]bool{ssarANP: c.nodes}}
			for b.Loop() {
				_ = s.networksWatermark(visible, g)
				_ = s.tasksWatermark(projects, c.nodes)
			}
		})
	}
}

// A grant or a revocation that leaves the visible set alone still moves the
// networks watermark: the pulls it covers read those verdicts too.
func TestNetworksWatermarkFollowsGrants(t *testing.T) {
	fc := newFakeCluster(t)
	var anp atomic.Bool
	fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		return a.Resource == "adminnetworkpolicies" && anp.Load()
	}
	s := fc.server(nil, Config{})
	id := auth.Identity{Token: tenantToken, Username: "tenant"}
	c, err := s.clusterF.For(id.Token)
	if err != nil {
		t.Fatal(err)
	}
	visible := map[string]bool{"team-a-dev": true}
	mark := func() uint64 {
		return s.networksWatermark(visible, s.netGrants(context.Background(), id, c, false))
	}
	before := mark()
	if mark() != before {
		t.Fatal("the watermark moved with nothing changed")
	}
	anp.Store(true)
	s.bus.Publish(eventbus.RBACChanged) // what the grant's watch reports
	if mark() == before {
		t.Error("gaining admin network policy authority left the watermark")
	}
}

// A frame re-validates its token on the authenticator's cache: a revoked
// token ends its stream (ErrRejected evicts it) and signals its consoles
// without waiting for a cached review to expire; a review outage withholds the
// frame, never as a pass and never as a sign-out.
func TestFrameRejectsARevokedToken(t *testing.T) {
	fc := newFakeCluster(t)
	fc.namespaces = []corev1.Namespace{{ObjectMeta: metav1.ObjectMeta{Name: "team-a-dev", Labels: map[string]string{"dotvirt.io/project": "team-a"}}}}
	fc.readsVMs = tenantReadsTeamA
	s := fc.server(nil, Config{})
	var alive, down atomic.Bool
	authenticator := func() *auth.Authenticator {
		kube := fake.NewSimpleClientset()
		kube.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if down.Load() {
				return true, nil, errors.New("apiserver unavailable")
			}
			tr := action.(k8stesting.CreateAction).GetObject().(*authnv1.TokenReview).DeepCopy()
			tr.Status = authnv1.TokenReviewStatus{Authenticated: alive.Load(), User: authnv1.UserInfo{Username: "tenant"}}
			return true, tr, nil
		})
		return auth.New(kube, []byte("0123456789abcdef0123456789abcdef"))
	}
	id := auth.Identity{Token: tenantToken, Username: "tenant"}

	alive.Store(true)
	s.auth = authenticator()
	if _, err := s.InventoryForIdentity(context.Background(), id); err != nil {
		t.Fatalf("a live token: %v", err)
	}
	down.Store(true)
	s.auth = authenticator() // the cached verdict expired
	if _, err := s.InventoryForIdentity(context.Background(), id); err == nil || errors.Is(err, auth.ErrRejected) {
		t.Fatalf("a review outage must withhold the frame and keep the session: %v", err)
	}
	if v := s.bus.Version(eventbus.SessionEnded); v != 0 {
		t.Fatalf("a review outage signalled %d session ends", v)
	}
	down.Store(false)
	alive.Store(false)
	if _, err := s.InventoryForIdentity(context.Background(), id); !errors.Is(err, auth.ErrRejected) {
		t.Fatalf("a revoked token built a frame: %v", err)
	}
	if v := s.bus.Version(eventbus.SessionEnded); v != 1 {
		t.Fatalf("a revoked token signalled %d session ends, want 1", v)
	}
}

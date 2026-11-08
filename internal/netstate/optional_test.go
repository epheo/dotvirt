package netstate

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/epheo/dotvirt/internal/cluster"
)

// saWith builds an SA client whose cluster serves the enactment kind (or
// not), answers its access review with allowed, and refuses every LIST of
// enactments when listForbidden. lists counts the LISTs that reached it.
func saWith(t *testing.T, served, allowed, listForbidden bool, lists *atomic.Int64) *cluster.Client {
	t.Helper()
	kube := fake.NewSimpleClientset()
	if served {
		kube.Discovery().(*fakediscovery.FakeDiscovery).Resources = []*metav1.APIResourceList{{
			GroupVersion: gvrNNCE.GroupVersion().String(),
			APIResources: []metav1.APIResource{{Name: gvrNNCE.Resource}},
		}}
	}
	kube.PrependReactor("create", "selfsubjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		r := a.(k8stesting.CreateAction).GetObject().(*authzv1.SelfSubjectAccessReview).DeepCopy()
		r.Status.Allowed = allowed
		return true, r, nil
	})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvrNNCE: "NodeNetworkConfigurationEnactmentList"})
	dyn.PrependReactor("list", gvrNNCE.Resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		lists.Add(1)
		if listForbidden {
			return true, nil, apierrors.NewForbidden(gvrNNCE.GroupResource(), "", nil)
		}
		return false, nil, nil
	})
	return cluster.NewClient(kube, nil, dyn)
}

func nnceSpec(s *Snapshot) (watchSpec, *atomic.Bool) {
	for i, w := range s.watches {
		if w.gvr == gvrNNCE {
			return w, &s.healthy[i]
		}
	}
	return watchSpec{}, nil
}

// A released operator may not grant the enactment read yet: the watch must
// then never start, and the catalog must not read as stale.
func TestEnactmentsForbiddenNeverStartNorStale(t *testing.T) {
	for _, c := range []struct {
		name            string
		served, allowed bool
	}{
		{"kind not served", false, true},
		{"list forbidden by RBAC", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var lists atomic.Int64
			s := New(saWith(t, c.served, c.allowed, false, &lists), nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w, flag := nnceSpec(s)
			s.watch(ctx, w, flag)
			time.Sleep(200 * time.Millisecond)
			if n := lists.Load(); n != 0 {
				t.Errorf("the gated watch listed %d times", n)
			}
			if !s.Healthy() {
				t.Error("a gated optional watch must not read as stale")
			}
		})
	}
}

// Even once started, a refused LIST (a grant pulled later) never flips the
// catalog's health: per-host failures are an enrichment, not its substance.
func TestEnactmentListFailureIsNotStaleness(t *testing.T) {
	var lists atomic.Int64
	s := New(saWith(t, true, true, true, &lists), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, flag := nnceSpec(s)
	s.watch(ctx, w, flag)
	deadline := time.Now().Add(5 * time.Second)
	for lists.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if lists.Load() == 0 {
		t.Fatal("the allowed watch never listed")
	}
	if !s.Healthy() {
		t.Error("a refused optional LIST must not mark the catalog stale")
	}
}

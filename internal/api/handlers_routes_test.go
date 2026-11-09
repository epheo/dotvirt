package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/routes"
)

// routesSource serves one HTTPRoute in team-a-dev, whose status names another
// tenant, and a watch on HTTPRoutes a test can write through.
type routesSource struct {
	mu     sync.Mutex
	routes *watch.FakeWatcher
}

func (*routesSource) HasAPIResource(gvr schema.GroupVersionResource) bool {
	return gvr.Resource == "gateways" || gvr.Resource == "httproutes"
}

func (*routesSource) Can(context.Context, string, string, string, string) (bool, error) {
	return true, nil
}

func tenantRoute(reason string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
		"spec": map[string]any{"parentRefs": []any{map[string]any{"name": "public", "namespace": "edge"}}},
		"status": map[string]any{"parents": []any{map[string]any{
			"parentRef": map[string]any{"name": "public", "namespace": "edge"},
			"conditions": []any{map[string]any{
				"type": "Accepted", "status": "False", "reason": reason, "message": "Taken by route billing/api",
			}},
		}}},
	}}
	u.SetNamespace("team-a-dev")
	u.SetName("shop")
	return u
}

func (f *routesSource) DynamicListWatch(gvr schema.GroupVersionResource) *cache.ListWatch {
	var items []unstructured.Unstructured
	if gvr.Resource == "httproutes" {
		items = []unstructured.Unstructured{*tenantRoute("HostnameConflict")}
	}
	return &cache.ListWatch{
		ListWithContextFunc: func(context.Context, metav1.ListOptions) (runtime.Object, error) {
			return &unstructured.UnstructuredList{Object: map[string]any{"apiVersion": "v1", "kind": "List"}, Items: items}, nil
		},
		WatchFuncWithContext: func(context.Context, metav1.ListOptions) (watch.Interface, error) {
			w := watch.NewFake()
			if gvr.Resource == "httproutes" {
				f.mu.Lock()
				f.routes = w
				f.mu.Unlock()
			}
			return w, nil
		},
	}
}

func (f *routesSource) write(t *testing.T, o *unstructured.Unstructured) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		f.mu.Lock()
		w := f.routes
		f.mu.Unlock()
		if w != nil {
			w.Modify(o)
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no watch on HTTPRoutes")
		}
	}
}

func routesServer(t *testing.T, fc *fakeCluster, src *routesSource) *Server {
	t.Helper()
	s := fc.server(nil, Config{})
	s.frontDoor = routes.New(src, s.bus, func() bool { return true })
	s.frontDoor.Run(t.Context())
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if len(s.frontDoor.HeldIn([]string{"team-a-dev"})) == 1 {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatal("the snapshot never held the route")
		}
	}
}

func routesVersion(t *testing.T, s *Server, token string) uint64 {
	t.Helper()
	r := asUser(httptest.NewRequest(http.MethodGet, "/", nil), token)
	id, c, err := s.userCluster(r)
	if err != nil {
		t.Fatal(err)
	}
	return s.routesWatermark(r.Context(), id, c, map[string]bool{"team-a-dev": true})
}

func TestGatewaysAreAskedOfTheCaller(t *testing.T) {
	s := newFakeCluster(t).server(nil, Config{})
	s.frontDoor = routes.New(nil, eventbus.New(), func() bool { return false })

	wantStatus(t, do(s, adminToken, http.MethodGet, "/api/networking/gateways", ""), http.StatusOK)
	wantStatus(t, do(s, viewerToken, http.MethodGet, "/api/networking/gateways", ""), http.StatusForbidden)
}

func TestRoutesNeedTheRightToListThem(t *testing.T) {
	for _, c := range []struct {
		name   string
		routes bool
		rows   int
		moves  bool
	}{
		{"VMs and routes", true, 1, true},
		{"VMs, no routes", false, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fc := newFakeCluster(t)
			fc.readsVMs = tenantReadsTeamA
			fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
				return token == tenantToken && c.routes && a.Verb == "list" &&
					a.Resource == "httproutes" && a.Namespace == "team-a-dev"
			}
			src := &routesSource{}
			s := routesServer(t, fc, src)

			rec := do(s, tenantToken, http.MethodGet, "/api/networking/routes", "")
			wantStatus(t, rec, http.StatusOK)
			if strings.Contains(rec.Body.String(), "billing") {
				t.Fatalf("a tenant read another tenant's name: %s", rec.Body)
			}
			var got []model.Route
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != c.rows {
				t.Fatalf("rows = %+v, want %d", got, c.rows)
			}

			before := routesVersion(t, s, tenantToken)
			gen := s.frontDoor.Gen("team-a-dev", "httproutes")
			src.write(t, tenantRoute("NotAllowedByListeners"))
			for deadline := time.Now().Add(5 * time.Second); s.frontDoor.Gen("team-a-dev", "httproutes") == gen; time.Sleep(time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatal("the write never reached the snapshot")
				}
			}
			if after := routesVersion(t, s, tenantToken); (after != before) != c.moves {
				t.Fatalf("version %d then %d, want moved=%v", before, after, c.moves)
			}
		})
	}
}

func TestAnOperatorReadsTheControllersMessage(t *testing.T) {
	fc := newFakeCluster(t)
	s := routesServer(t, fc, &routesSource{})

	rec := do(s, adminToken, http.MethodGet, "/api/networking/gateways", "")
	wantStatus(t, rec, http.StatusOK)
	var got model.Gateways
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Routes) != 1 || got.Routes[0].Parents[0].Message == "" {
		t.Fatalf("routes = %+v", got.Routes)
	}
}

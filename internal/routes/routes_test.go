package routes

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/eventbus"
)

const leak = "Taken by route billing/api"

func obj(ns, name string, content map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: content}
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func cond(typ, status, reason, message string) any {
	return map[string]any{"type": typ, "status": status, "reason": reason, "message": message}
}

func httpRoute(ns, name string, accepted bool) *unstructured.Unstructured {
	status, reason, message := "True", "Accepted", ""
	if !accepted {
		status, reason, message = "False", "HostnameConflict", leak
	}
	return obj(ns, name, map[string]any{
		"spec": map[string]any{
			"hostnames":  []any{name + ".apps.example.com"},
			"parentRefs": []any{map[string]any{"name": "public", "namespace": "edge", "sectionName": "https"}},
			"rules": []any{map[string]any{"backendRefs": []any{
				map[string]any{"name": "svc-a", "port": int64(8080)},
				map[string]any{"name": "svc-b", "port": int64(8080), "weight": int64(3)},
				map[string]any{"kind": "Bucket", "name": "not-a-service"},
			}}},
		},
		"status": map[string]any{"parents": []any{map[string]any{
			"parentRef":  map[string]any{"name": "public", "namespace": "edge", "sectionName": "https"},
			"conditions": []any{cond("Accepted", status, reason, message), cond("ResolvedRefs", "True", "ResolvedRefs", "")},
		}}},
	})
}

func gateway() *unstructured.Unstructured {
	return obj("edge", "public", map[string]any{
		"spec": map[string]any{"gatewayClassName": "portail", "listeners": []any{
			map[string]any{"name": "https", "port": int64(443), "protocol": "HTTPS", "hostname": "*.apps.example.com"},
			map[string]any{"name": "ssh", "port": int64(2222), "protocol": "TCP"},
		}},
		"status": map[string]any{
			"addresses":  []any{map[string]any{"value": "192.0.2.10"}},
			"conditions": []any{cond("Accepted", "True", "Accepted", ""), cond("Programmed", "True", "Programmed", "")},
			"listeners": []any{map[string]any{
				"name": "https", "attachedRoutes": int64(6),
				"conditions": []any{cond("Accepted", "True", "Accepted", ""), cond("Programmed", "True", "Programmed", "")},
			}},
		},
	})
}

func newSnapshot(t *testing.T) (*Snapshot, *eventbus.Bus) {
	t.Helper()
	bus := eventbus.New()
	return New(nil, bus, func() bool { return true }), bus
}

func mustAdd(t *testing.T, s interface{ Add(any) error }, o any) {
	t.Helper()
	if err := s.Add(o); err != nil {
		t.Fatal(err)
	}
}

func all(string, string) bool { return true }

func TestGatewayReadsItsListeners(t *testing.T) {
	g, _ := decodeGateway(gateway())
	if g.Class != "portail" || !g.Ready || len(g.Addresses) != 1 || g.Addresses[0] != "192.0.2.10" {
		t.Fatalf("gateway = %+v", g)
	}
	if l := g.Listeners[0]; !l.Ready || l.AttachedRoutes != 6 || l.Port != 443 {
		t.Fatalf("https = %+v", l)
	}
	if l := g.Listeners[1]; l.Ready || l.Reason != "Pending" {
		t.Fatalf("a listener with no status reads as ready: %+v", l)
	}
}

func TestRouteJoinsSpecAndStatus(t *testing.T) {
	r := decodeRoute("HTTPRoute", httpRoute("web", "shop", true))
	if len(r.Parents) != 1 || !r.Parents[0].Attached || r.Parents[0].Namespace != "edge" {
		t.Fatalf("parents = %+v", r.Parents)
	}
	if len(r.Backends) != 2 || r.Backends[0].Namespace != "web" || r.Backends[0].Weight != 1 || r.Backends[1].Weight != 3 {
		t.Fatalf("backends = %+v", r.Backends)
	}
}

func TestAParentWithNoStatusIsNotAttached(t *testing.T) {
	r := decodeRoute("HTTPRoute", obj("web", "new", map[string]any{"spec": map[string]any{
		"parentRefs": []any{map[string]any{"name": "public"}},
	}}))
	if r.Parents[0].Attached || r.Parents[0].Reason != "Pending" || r.Parents[0].Namespace != "web" {
		t.Fatalf("parent = %+v", r.Parents[0])
	}
}

func TestATenantNeverReadsAControllersMessage(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.routes[0], httpRoute("web", "shop", false))
	got := s.Routes(map[Held]bool{{"web", "httproutes"}: true})
	if len(got) != 1 || got[0].Parents[0].Reason != "HostnameConflict" {
		t.Fatalf("routes = %+v", got)
	}
	body, _ := json.Marshal(got)
	if strings.Contains(string(body), "billing") {
		t.Fatalf("a tenant row carries another tenant's name: %s", body)
	}
	// The strip is on a copy: an operator still reads the row whole.
	over := s.Overview(all)
	if over.Routes[0].Parents[0].Message != leak {
		t.Fatalf("the shared row was changed: %+v", over.Routes[0].Parents[0])
	}
}

func TestRoutesShowOnlyWhatWasReviewed(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.routes[0], httpRoute("web", "shop", true))
	mustAdd(t, s.routes[0], httpRoute("billing", "api", true))
	mustAdd(t, s.routes[2], httpRoute("web", "ssh", true))

	got := s.Routes(map[Held]bool{{"web", "httproutes"}: true})
	if len(got) != 1 || got[0].Name != "shop" {
		t.Fatalf("routes = %+v", got)
	}
	held := s.HeldIn([]string{"web"})
	if len(held) != 2 {
		t.Fatalf("held = %+v", held)
	}
}

func TestOverviewAsksForEachOptionalKind(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.gateways, gateway())
	mustAdd(t, s.routes[0], httpRoute("web", "shop", true))
	mustAdd(t, s.routes[2], httpRoute("web", "ssh", true))
	mustAdd(t, s.limits, obj("edge", "limit", map[string]any{"spec": map[string]any{
		"targetRef": map[string]any{"name": "public", "sectionName": "https"}, "requestsPerSecond": int64(20),
	}}))

	got := s.Overview(func(_, resource string) bool { return resource == "httproutes" })
	if len(got.Routes) != 1 || got.Routes[0].Kind != "HTTPRoute" {
		t.Fatalf("routes = %+v", got.Routes)
	}
	if len(got.Gateways[0].RateLimits) != 0 {
		t.Fatalf("a rate limit the caller may not list: %+v", got.Gateways[0].RateLimits)
	}

	got = s.Overview(all)
	if rl := got.Gateways[0].RateLimits; len(rl) != 1 || rl[0].Listener != "https" || rl[0].Burst != 20 {
		t.Fatalf("rate limits = %+v", rl)
	}
}

// A review can reach the apiserver: held under the lock, a slow one would stall
// the frame path behind a waiting writer.
func TestOverviewReviewsWithTheLockFree(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.routes[0], httpRoute("web", "shop", true))
	mustAdd(t, s.limits, obj("edge", "limit", map[string]any{"spec": map[string]any{
		"targetRef": map[string]any{"name": "public"}, "requestsPerSecond": int64(20),
	}}))
	done := make(chan struct{})
	go func() {
		s.Overview(func(string, string) bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return true
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a review ran under the snapshot's lock")
	}
}

func TestATenantsLevelMovesWithItsOwnRoutesOnly(t *testing.T) {
	s, bus := newSnapshot(t)
	mustAdd(t, s.routes[0], httpRoute("web", "shop", true))
	mustAdd(t, s.routes[0], httpRoute("billing", "api", true))
	web, total := s.Gen("web", "httproutes"), s.Total()

	if err := s.routes[0].Update(httpRoute("billing", "api", false)); err != nil {
		t.Fatal(err)
	}
	if s.Gen("web", "httproutes") != web {
		t.Fatal("another tenant's route moved this tenant's level")
	}
	if s.Total() == total {
		t.Fatal("an operator's level did not move")
	}

	before := bus.Version(eventbus.RoutesChanged)
	again := httpRoute("billing", "api", false)
	again.SetResourceVersion("99")
	if err := s.routes[0].Update(again); err != nil {
		t.Fatal(err)
	}
	if bus.Version(eventbus.RoutesChanged) != before {
		t.Fatal("a write that moved nothing published")
	}
}

func TestSameNameInTwoNamespacesKeepsBothRoutes(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.routes[0], httpRoute("a", "same", true))
	mustAdd(t, s.routes[0], httpRoute("b", "same", true))
	if got := s.Overview(all); len(got.Routes) != 2 {
		t.Fatalf("routes = %+v", got.Routes)
	}
}

func TestSyncedWaitsForGatewaysAndHTTPRoutesOnly(t *testing.T) {
	s, _ := newSnapshot(t)
	if err := s.gateways.Replace(nil, "1"); err != nil {
		t.Fatal(err)
	}
	if s.Overview(all).Synced {
		t.Fatal("synced before HTTPRoutes listed")
	}
	if err := s.routes[0].Replace(nil, "1"); err != nil {
		t.Fatal(err)
	}
	if !s.Overview(all).Synced {
		t.Fatal("waits on a kind that may not be served")
	}
}

type fakeSource struct {
	mu      sync.Mutex
	served  map[string]bool
	watched map[string]bool
	ended   map[string]bool
}

func (f *fakeSource) HasAPIResource(gvr schema.GroupVersionResource) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.served[gvr.Resource+"/"+gvr.Version]
}

func (f *fakeSource) Can(context.Context, string, string, string, string) (bool, error) {
	return true, nil
}

func (f *fakeSource) DynamicListWatch(gvr schema.GroupVersionResource) *cache.ListWatch {
	id := gvr.Resource + "/" + gvr.Version
	return &cache.ListWatch{
		ListWithContextFunc: func(context.Context, metav1.ListOptions) (runtime.Object, error) {
			return &unstructured.UnstructuredList{Object: map[string]any{"apiVersion": "v1", "kind": "List"}}, nil
		},
		WatchFuncWithContext: func(ctx context.Context, _ metav1.ListOptions) (watch.Interface, error) {
			f.mu.Lock()
			f.watched[id] = true
			f.mu.Unlock()
			w := watch.NewFake()
			go func() {
				<-ctx.Done()
				f.mu.Lock()
				f.ended[id] = true
				f.mu.Unlock()
				w.Stop()
			}()
			return w, nil
		},
	}
}

func (f *fakeSource) has(m map[string]bool, id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return m[id]
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAKindIsWatchedAtTheVersionServedAndStopsWithTheModule(t *testing.T) {
	src := &fakeSource{
		served:  map[string]bool{"gateways/v1": true, "httproutes/v1": true, "tlsroutes/v1alpha3": true},
		watched: map[string]bool{}, ended: map[string]bool{},
	}
	savedPace, savedProbe := pace, probe
	pace, probe = time.Millisecond, time.Millisecond
	t.Cleanup(func() { pace, probe = savedPace, savedProbe })
	var mu sync.Mutex
	on := true
	s := New(src, eventbus.New(), func() bool { mu.Lock(); defer mu.Unlock(); return on })
	s.Run(t.Context())

	eventually(t, "tlsroutes at its served version", func() bool { return src.has(src.watched, "tlsroutes/v1alpha3") })
	eventually(t, "synced", func() bool { return s.Overview(all).Synced })
	if src.has(src.watched, "tlsroutes/v1") || src.has(src.watched, "tcproutes/v1alpha2") {
		t.Fatal("watched a version that is not served")
	}

	mu.Lock()
	on = false
	mu.Unlock()
	eventually(t, "the watches ended", func() bool {
		return src.has(src.ended, "gateways/v1") && src.has(src.ended, "tlsroutes/v1alpha3")
	})
	eventually(t, "not started", func() bool { return !s.Started() })
	if s.Overview(all).Synced {
		t.Fatal("a stopped module reads as synced")
	}
}

// Only portail's condition, True for the class's current generation, says a
// class's Gateways are isolated.
func TestDecodeIsolated(t *testing.T) {
	class := func(conds ...map[string]any) *unstructured.Unstructured {
		list := make([]any, len(conds))
		for i, c := range conds {
			list[i] = c
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"name": "portail", "generation": int64(3)},
			"status":   map[string]any{"conditions": list},
		}}
	}
	cond := func(kind, status string, gen int64) map[string]any {
		return map[string]any{"type": kind, "status": status, "observedGeneration": gen}
	}
	for name, c := range map[string]struct {
		u    *unstructured.Unstructured
		want bool
	}{
		"true":       {class(cond("Accepted", "True", 3), cond(Isolated, "True", 3)), true},
		"stale":      {class(cond(Isolated, "True", 2)), false},
		"no gen":     {class(map[string]any{"type": Isolated, "status": "True"}), false},
		"false":      {class(cond(Isolated, "False", 3)), false},
		"absent":     {class(cond("Accepted", "True", 3)), false},
		"other kind": {class(cond("GatewaysIsolated", "True", 3)), false},
	} {
		if got, keep := decodeIsolated(c.u); got != c.want || !keep {
			t.Errorf("%s: %v %v, want %v", name, got, keep, c.want)
		}
	}
}

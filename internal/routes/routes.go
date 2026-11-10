// Package routes is the read side of the cluster's Gateways: an SA-held
// snapshot of Gateway API Gateways and routes, and portail's rate limits, so a
// caller's pull filters in memory and no request reaches the cluster.
//
// Two reads leave here. Overview shows every Gateway and route, for a caller
// whose own token may list them across namespaces. Routes shows the routes a
// caller may list in its own namespaces, without the status messages: a
// controller's conflict text can name another tenant's route.
package routes

import (
	"context"
	"log"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/reflect"
)

const group = "gateway.networking.k8s.io"

// The kinds outside the route tables.
var (
	GVRGateway   = schema.GroupVersionResource{Group: group, Version: "v1", Resource: "gateways"}
	GVRClass     = schema.GroupVersionResource{Group: group, Version: "v1", Resource: "gatewayclasses"}
	GVRRateLimit = schema.GroupVersionResource{Group: "portail.epheo.eu", Version: "v1alpha1", Resource: "ratelimitpolicies"}
)

// RouteKind is one route kind. Experimental kinds change version between
// Gateway API releases, so each lists what it may be served as, newest first.
type RouteKind struct {
	Kind     string
	Resource string
	Versions []string
}

// RouteKinds are the kinds the console shows.
var RouteKinds = []RouteKind{
	{"HTTPRoute", "httproutes", []string{"v1"}},
	{"TLSRoute", "tlsroutes", []string{"v1", "v1alpha3", "v1alpha2"}},
	{"TCPRoute", "tcproutes", []string{"v1alpha2"}},
	{"UDPRoute", "udproutes", []string{"v1alpha2"}},
}

// Group is the API group of the Gateways and route kinds.
const Group = group

// Source is the cluster as the snapshot reads it; *cluster.Client in production.
type Source interface {
	HasAPIResource(schema.GroupVersionResource) bool
	DynamicListWatch(schema.GroupVersionResource) *cache.ListWatch
	Can(ctx context.Context, verb, group, resource, namespace string) (bool, error)
}

// pace is how often an idle snapshot rereads the module flag: an in-memory read
// on a cluster without routes.
var pace = 10 * time.Second

// probe is how often a route kind not served yet is looked for.
var probe = time.Minute

type rateLimit struct {
	Namespace, Gateway, Listener string
	RequestsPerSecond, Burst     int32
}

// Snapshot holds the projections. Build with New, start with Run; reads are
// safe for concurrent callers.
type Snapshot struct {
	src    Source
	bus    *eventbus.Bus
	served func() bool

	mu       sync.RWMutex
	gateways *reflect.Table[model.GatewayInfo]
	limits   *reflect.Table[rateLimit]
	// isolated is, by GatewayClass, whether its controller says one Gateway
	// can never delay another's configuration (Isolated).
	isolated *reflect.Table[bool]
	routes   []*reflect.Table[model.Route]
	// gens counts each namespace's moves per route kind, so a tenant's
	// watermark moves with the routes it may read; total counts every move.
	gens  map[string]uint64
	total uint64

	started atomic.Bool
	healthy []atomic.Bool
}

func genKey(namespace, resource string) string { return namespace + "\x00" + resource }

// New builds the snapshot. served is the module flag, which already reads the
// cluster, so a cluster without a GatewayClass pays no API call here.
func New(src Source, bus *eventbus.Bus, served func() bool) *Snapshot {
	s := &Snapshot{src: src, bus: bus, served: served, gens: map[string]uint64{}}
	s.gateways = &reflect.Table[model.GatewayInfo]{Lock: &s.mu, Decode: decodeGateway, Key: reflect.NamespacedKey, Publish: s.publish,
		Touch: func(string, *model.GatewayInfo, *model.GatewayInfo) { s.total++ }}
	s.limits = &reflect.Table[rateLimit]{Lock: &s.mu, Decode: decodeRateLimit, Key: reflect.NamespacedKey, Publish: s.publish,
		Touch: func(string, *rateLimit, *rateLimit) { s.total++ }}
	s.isolated = &reflect.Table[bool]{Lock: &s.mu, Decode: decodeIsolated, Publish: s.publish,
		Touch: func(string, *bool, *bool) { s.total++ }}
	for _, k := range RouteKinds {
		s.routes = append(s.routes, &reflect.Table[model.Route]{
			Lock: &s.mu, Key: reflect.NamespacedKey, Publish: s.publish,
			Decode: func(u *unstructured.Unstructured) (model.Route, bool) { return decodeRoute(k.Kind, u), true },
			Touch: func(_ string, old, now *model.Route) {
				for _, r := range []*model.Route{old, now} {
					if r != nil {
						s.gens[genKey(r.Namespace, k.Resource)]++
					}
				}
				s.total++
			},
		})
	}
	s.healthy = make([]atomic.Bool, 3+len(RouteKinds))
	for i := range s.healthy {
		s.healthy[i].Store(true)
	}
	return s
}

// Run keeps one reflector per kind running while the module is on, and returns
// at once.
func (s *Snapshot) Run(ctx context.Context) {
	reflect.RunWhile(ctx, pace, func() bool { return s.ready(ctx) }, s.served, s.start, s.clear)
}

func (s *Snapshot) ready(ctx context.Context) bool {
	if !s.served() {
		return false
	}
	want := []schema.GroupVersionResource{GVRGateway, GVRRateLimit, GVRClass}
	for _, k := range RouteKinds {
		want = append(want, schema.GroupVersionResource{Group: group, Resource: k.Resource})
	}
	for _, gvr := range want {
		ok, err := s.src.Can(ctx, "list", gvr.Group, gvr.Resource, "")
		if err != nil {
			log.Printf("routes: review list %s: %v", gvr.Resource, err)
			return false
		}
		if !ok {
			return false
		}
	}
	return true
}

func (s *Snapshot) start(ctx context.Context) {
	s.started.Store(true)
	s.publish()
	reflect.RunWhenServed(ctx, s.src.HasAPIResource, GVRGateway, s.src.DynamicListWatch(GVRGateway), s.gateways, &s.healthy[0], s.flipped)
	reflect.RunWhenServed(ctx, s.src.HasAPIResource, GVRRateLimit, s.src.DynamicListWatch(GVRRateLimit), s.limits, &s.healthy[1], s.flipped)
	reflect.RunWhenServed(ctx, s.src.HasAPIResource, GVRClass, s.src.DynamicListWatch(GVRClass), s.isolated, &s.healthy[2], s.flipped)
	for i, k := range RouteKinds {
		go s.watchRoute(ctx, k, s.routes[i], &s.healthy[3+i])
	}
}

// watchRoute waits for one of the kind's versions to be served, as an
// experimental kind may be installed later or under a newer version.
func (s *Snapshot) watchRoute(ctx context.Context, k RouteKind, store cache.Store, healthy *atomic.Bool) {
	t := time.NewTicker(probe)
	defer t.Stop()
	for {
		for _, v := range k.Versions {
			gvr := schema.GroupVersionResource{Group: group, Version: v, Resource: k.Resource}
			if s.src.HasAPIResource(gvr) {
				reflect.Run(ctx, reflect.TrackHealth(s.src.DynamicListWatch(gvr), healthy, s.flipped), store)
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Snapshot) clear() {
	s.started.Store(false)
	s.gateways.Clear()
	s.limits.Clear()
	s.isolated.Clear()
	for _, t := range s.routes {
		t.Clear()
	}
	s.mu.Lock()
	s.total++
	s.mu.Unlock()
	s.publish()
}

func (s *Snapshot) publish() {
	if s.bus != nil {
		s.bus.Publish(eventbus.RoutesChanged)
	}
}

// flipped moves the operator's watermark, which is all that shows staleness.
func (s *Snapshot) flipped() {
	s.mu.Lock()
	s.total++
	s.mu.Unlock()
	s.publish()
}

// Started reports whether the reflectors run: until then every read is empty
// and a frame needs no watermark.
func (s *Snapshot) Started() bool { return s.started.Load() }

// Total is the level of everything an operator's pull reads.
func (s *Snapshot) Total() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.total
}

// Gen is the level of one route kind in one namespace.
func (s *Snapshot) Gen(namespace, resource string) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gens[genKey(namespace, resource)]
}

// Held is one route kind present in one namespace.
type Held struct{ Namespace, Resource string }

// HeldIn lists the kinds that have rows in namespaces, so a caller is reviewed
// only for what it could be shown.
func (s *Snapshot) HeldIn(namespaces []string) []Held {
	wanted := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		wanted[ns] = true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	found := map[Held]bool{}
	for i, t := range s.routes {
		for _, r := range t.Rows {
			if wanted[r.Namespace] {
				found[Held{r.Namespace, RouteKinds[i].Resource}] = true
			}
		}
	}
	out := make([]Held, 0, len(found))
	for h := range found {
		out = append(out, h)
	}
	return out
}

// Routes is the routes of the namespace and kind pairs in readable, with every
// status message removed.
func (s *Snapshot) Routes(readable map[Held]bool) []model.Route {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []model.Route{}
	for i, t := range s.routes {
		for _, r := range t.Rows {
			if !readable[Held{r.Namespace, RouteKinds[i].Resource}] {
				continue
			}
			r.Parents = slices.Clone(r.Parents)
			for j := range r.Parents {
				r.Parents[j].Message = ""
			}
			out = append(out, r)
		}
	}
	sortRoutes(out)
	return out
}

func sortRoutes(rs []model.Route) {
	slices.SortFunc(rs, func(a, b model.Route) int {
		return strings.Compare(a.Namespace+"\x00"+a.Name+"\x00"+a.Kind, b.Namespace+"\x00"+b.Name+"\x00"+b.Kind)
	})
}

// Overview is every Gateway and route. sees answers for each kind the caller
// was not already reviewed for; it may reach the cluster, so it runs before the
// lock, which the frame path's reads wait on.
func (s *Snapshot) Overview(sees func(group, resource string) bool) model.Gateways {
	limits := sees(GVRRateLimit.Group, GVRRateLimit.Resource)
	kinds := make([]bool, len(RouteKinds))
	for i, k := range RouteKinds {
		kinds[i] = sees(group, k.Resource)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := model.Gateways{
		Synced:   s.gateways.Synced() && s.routes[0].Synced(),
		Stale:    !reflect.AllHealthy(s.healthy),
		Gateways: make([]model.GatewayInfo, 0, len(s.gateways.Rows)),
		Routes:   []model.Route{},
	}
	byGateway := map[string][]model.GatewayRateLimit{}
	if limits {
		for _, l := range s.limits.Rows {
			key := l.Namespace + "/" + l.Gateway
			byGateway[key] = append(byGateway[key], model.GatewayRateLimit{Listener: l.Listener, RequestsPerSecond: l.RequestsPerSecond, Burst: l.Burst})
		}
	}
	for key, g := range s.gateways.Rows {
		g.RateLimits = byGateway[key]
		slices.SortFunc(g.RateLimits, func(a, b model.GatewayRateLimit) int { return strings.Compare(a.Listener, b.Listener) })
		out.Gateways = append(out.Gateways, g)
	}
	slices.SortFunc(out.Gateways, func(a, b model.GatewayInfo) int {
		return strings.Compare(a.Namespace+"\x00"+a.Name, b.Namespace+"\x00"+b.Name)
	})
	for i, t := range s.routes {
		if !kinds[i] {
			continue
		}
		for _, r := range t.Rows {
			out.Routes = append(out.Routes, r)
		}
	}
	sortRoutes(out.Routes)
	return out
}

func decode[T any](u *unstructured.Unstructured) T {
	var doc T
	// The apiserver validated the object: a field that fails to convert is
	// near impossible, and those converted before it still show the row.
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &doc)
	return doc
}

type conditionDoc struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason"`
	Message            string `json:"message"`
	ObservedGeneration int64  `json:"observedGeneration"`
}

// verdict folds conditions into whether every wanted one holds, and the first
// that does not.
func verdict(conds []conditionDoc, wanted ...string) (ok bool, reason, message string) {
	ok = true
	for _, want := range wanted {
		i := slices.IndexFunc(conds, func(c conditionDoc) bool { return c.Type == want })
		if i < 0 {
			if ok {
				ok, reason = false, "Pending"
			}
			continue
		}
		if conds[i].Status != "True" && ok {
			ok, reason, message = false, conds[i].Reason, conds[i].Message
		}
	}
	return ok, reason, message
}

type gatewayDoc struct {
	Spec struct {
		GatewayClassName string `json:"gatewayClassName"`
		Listeners        []struct {
			Name     string `json:"name"`
			Port     int32  `json:"port"`
			Protocol string `json:"protocol"`
			Hostname string `json:"hostname"`
		} `json:"listeners"`
	} `json:"spec"`
	Status struct {
		Addresses []struct {
			Value string `json:"value"`
		} `json:"addresses"`
		Conditions []conditionDoc `json:"conditions"`
		Listeners  []struct {
			Name           string         `json:"name"`
			AttachedRoutes int32          `json:"attachedRoutes"`
			Conditions     []conditionDoc `json:"conditions"`
		} `json:"listeners"`
	} `json:"status"`
}

// Isolated is portail's GatewayClass condition saying one Gateway can never
// delay another's configuration: each holds a bounded share of the
// controller, and tokens are verified before any shared budget is spent.
const Isolated = "portail.epheo.eu/GatewaysIsolated"

// GatewaysIsolated reports whether GatewayClass class says Isolated is True
// for its current generation; false while it is unknown. A True left from an
// older generation is a controller that stopped reconciling the class, or one
// that no longer writes the condition.
func (s *Snapshot) GatewaysIsolated(class string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isolated.Rows[class]
}

func decodeIsolated(u *unstructured.Unstructured) (bool, bool) {
	doc := decode[struct {
		Status struct {
			Conditions []conditionDoc `json:"conditions"`
		} `json:"status"`
	}](u)
	return slices.ContainsFunc(doc.Status.Conditions, func(c conditionDoc) bool {
		return c.Type == Isolated && c.Status == "True" && c.ObservedGeneration == u.GetGeneration()
	}), true
}

func decodeGateway(u *unstructured.Unstructured) (model.GatewayInfo, bool) {
	doc := decode[gatewayDoc](u)
	g := model.GatewayInfo{Namespace: u.GetNamespace(), Name: u.GetName(), Class: doc.Spec.GatewayClassName}
	for _, a := range doc.Status.Addresses {
		g.Addresses = append(g.Addresses, a.Value)
	}
	g.Ready, g.Reason, g.Message = verdict(doc.Status.Conditions, "Accepted", "Programmed")
	for _, l := range doc.Spec.Listeners {
		out := model.GatewayListener{Name: l.Name, Port: l.Port, Protocol: l.Protocol, Hostname: l.Hostname, Reason: "Pending"}
		i := slices.IndexFunc(doc.Status.Listeners, func(st struct {
			Name           string         `json:"name"`
			AttachedRoutes int32          `json:"attachedRoutes"`
			Conditions     []conditionDoc `json:"conditions"`
		}) bool {
			return st.Name == l.Name
		})
		if i >= 0 {
			st := doc.Status.Listeners[i]
			out.AttachedRoutes = st.AttachedRoutes
			out.Ready, out.Reason, out.Message = verdict(st.Conditions, "Accepted", "Programmed")
		}
		g.Listeners = append(g.Listeners, out)
	}
	return g, true
}

type rateLimitDoc struct {
	Spec struct {
		TargetRef struct {
			Name        string `json:"name"`
			SectionName string `json:"sectionName"`
		} `json:"targetRef"`
		RequestsPerSecond int32 `json:"requestsPerSecond"`
		Burst             int32 `json:"burst"`
	} `json:"spec"`
}

func decodeRateLimit(u *unstructured.Unstructured) (rateLimit, bool) {
	doc := decode[rateLimitDoc](u)
	burst := doc.Spec.Burst
	if burst == 0 {
		burst = doc.Spec.RequestsPerSecond
	}
	return rateLimit{
		Namespace: u.GetNamespace(), Gateway: doc.Spec.TargetRef.Name, Listener: doc.Spec.TargetRef.SectionName,
		RequestsPerSecond: doc.Spec.RequestsPerSecond, Burst: burst,
	}, true
}

type parentRefDoc struct {
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	SectionName string `json:"sectionName"`
}

type routeDoc struct {
	Spec struct {
		Hostnames  []string       `json:"hostnames"`
		ParentRefs []parentRefDoc `json:"parentRefs"`
		Rules      []struct {
			BackendRefs []struct {
				Kind      string `json:"kind"`
				Namespace string `json:"namespace"`
				Name      string `json:"name"`
				Port      int32  `json:"port"`
				Weight    *int32 `json:"weight"`
			} `json:"backendRefs"`
		} `json:"rules"`
	} `json:"spec"`
	Status struct {
		Parents []struct {
			ParentRef  parentRefDoc   `json:"parentRef"`
			Conditions []conditionDoc `json:"conditions"`
		} `json:"parents"`
	} `json:"status"`
}

func decodeRoute(kind string, u *unstructured.Unstructured) model.Route {
	doc := decode[routeDoc](u)
	r := model.Route{Kind: kind, Namespace: u.GetNamespace(), Name: u.GetName(), Hostnames: doc.Spec.Hostnames}
	orOwn := func(ns string) string {
		if ns == "" {
			return u.GetNamespace()
		}
		return ns
	}
	for _, ref := range doc.Spec.ParentRefs {
		p := model.RouteParent{Namespace: orOwn(ref.Namespace), Name: ref.Name, Section: ref.SectionName, Reason: "Pending"}
		for _, st := range doc.Status.Parents {
			if orOwn(st.ParentRef.Namespace) == p.Namespace && st.ParentRef.Name == p.Name && st.ParentRef.SectionName == p.Section {
				p.Attached, p.Reason, p.Message = verdict(st.Conditions, "Accepted", "ResolvedRefs")
			}
		}
		r.Parents = append(r.Parents, p)
	}
	for _, rule := range doc.Spec.Rules {
		for _, b := range rule.BackendRefs {
			if b.Kind != "" && b.Kind != "Service" {
				continue
			}
			weight := int32(1)
			if b.Weight != nil {
				weight = *b.Weight
			}
			r.Backends = append(r.Backends, model.RouteBackend{Namespace: orOwn(b.Namespace), Name: b.Name, Port: b.Port, Weight: weight})
		}
	}
	return r
}

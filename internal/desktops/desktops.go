// Package desktops is the read side of DeskuBe: an SA-held snapshot of its
// classes, pools, desktops and claims, so a caller's pull filters in memory
// and no request reaches the cluster.
//
// One read leaves here, Overview: each namespaced kind where the caller may
// list it, and the classes for a caller who may list them cluster-wide.
package desktops

import (
	"cmp"
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

// Group is DeskuBe's API group.
const Group = "vdi.desku.be"

const version = "v1alpha1"

// The kinds the snapshot watches. Instance is the one whose presence turns the
// module on.
var (
	GVRClass    = schema.GroupVersionResource{Group: Group, Version: version, Resource: "vdiclasses"}
	GVRPool     = schema.GroupVersionResource{Group: Group, Version: version, Resource: "vdipools"}
	GVRInstance = schema.GroupVersionResource{Group: Group, Version: version, Resource: "vdiinstances"}
	GVRClaim    = schema.GroupVersionResource{Group: Group, Version: version, Resource: "vdiclaims"}
)

// Source is the cluster as the snapshot reads it; *cluster.Client in production.
type Source interface {
	HasAPIResource(schema.GroupVersionResource) bool
	DynamicListWatch(schema.GroupVersionResource) *cache.ListWatch
	Can(ctx context.Context, verb, group, resource, namespace string) (bool, error)
}

// pace is how often an idle snapshot rereads the module flag: an in-memory read
// on a cluster without DeskuBe, one review of the SA's rights where an operator
// release has not granted them yet.
var pace = 10 * time.Second

// Held is one kind present in one namespace. The classes are held at "".
type Held struct{ Namespace, Resource string }

// kind is one watched kind and its projection.
type kind struct {
	gvr   schema.GroupVersionResource
	table interface {
		cache.Store
		Synced() bool
		Clear()
	}
}

// Snapshot holds the projections. Build with New, start with Run; reads are
// safe for concurrent callers.
type Snapshot struct {
	src    Source
	bus    *eventbus.Bus
	served func() bool

	mu        sync.RWMutex
	classes   *reflect.Table[model.DesktopClass]
	pools     *reflect.Table[model.DesktopPool]
	instances *reflect.Table[model.Desktop]
	claims    *reflect.Table[model.DesktopClaim]
	// gens counts each kind's moves per namespace, so a caller's watermark
	// moves with what it may list only. held counts the rows behind each.
	gens map[Held]uint64
	held map[Held]int
	// flips counts what every caller's pull shows besides rows: a watch's
	// health, and the module stopping.
	flips uint64

	started atomic.Bool
	// healthy and present follow the order of kinds.
	healthy [4]atomic.Bool
	present [4]atomic.Bool
}

// kinds are the watched kinds, in the order of healthy and present.
func (s *Snapshot) kinds() []kind {
	return []kind{{GVRClass, s.classes}, {GVRPool, s.pools}, {GVRInstance, s.instances}, {GVRClaim, s.claims}}
}

// New builds the snapshot. served is the module flag, which already reads
// discovery, so a cluster without DeskuBe pays no API call here.
func New(src Source, bus *eventbus.Bus, served func() bool) *Snapshot {
	s := &Snapshot{src: src, bus: bus, served: served, gens: map[Held]uint64{}, held: map[Held]int{}}
	s.classes = newTable(s, GVRClass.Resource, decodeClass, nil)
	s.pools = newTable(s, GVRPool.Resource, decodePool, reflect.NamespacedKey)
	s.instances = newTable(s, GVRInstance.Resource, decodeInstance, reflect.NamespacedKey)
	s.claims = newTable(s, GVRClaim.Resource, decodeClaim, reflect.NamespacedKey)
	for i := range s.healthy {
		s.healthy[i].Store(true)
	}
	return s
}

func newTable[V any](s *Snapshot, resource string, decode func(*unstructured.Unstructured) (V, bool), key func(*unstructured.Unstructured) string) *reflect.Table[V] {
	return &reflect.Table[V]{Lock: &s.mu, Decode: decode, Key: key, Publish: s.publish,
		Touch: func(k string, old, now *V) {
			h := Held{Resource: resource}
			if key != nil {
				h.Namespace, _, _ = strings.Cut(k, "/")
			}
			s.gens[h]++
			switch {
			case old == nil:
				s.held[h]++
			case now == nil:
				if s.held[h]--; s.held[h] == 0 {
					delete(s.held, h)
				}
			}
		}}
}

// Run keeps one reflector per kind running while the module is on, and returns
// at once. Off stops them, so an uninstalled DeskuBe leaves no watch retrying.
func (s *Snapshot) Run(ctx context.Context) {
	reflect.RunWhile(ctx, pace, func() bool { return s.ready(ctx) }, s.served, s.start, s.clear)
}

// start reads which kinds are served before any list lands, as Synced waits
// for those only: a DeskuBe release missing a kind leaves no failing watch,
// and no pull that never syncs. One served later is watched from then on.
func (s *Snapshot) start(ctx context.Context) {
	kinds := s.kinds()
	for i, k := range kinds {
		s.present[i].Store(s.src.HasAPIResource(k.gvr))
	}
	s.started.Store(true)
	s.publish()
	for i, k := range kinds {
		reflect.RunWhenServed(ctx, reflect.Served(s.src.HasAPIResource, &s.present[i]), k.gvr, s.src.DynamicListWatch(k.gvr), k.table, &s.healthy[i], s.flipped)
	}
}

// clear forgets what a stopped module's watches left.
func (s *Snapshot) clear() {
	s.started.Store(false)
	for i, k := range s.kinds() {
		k.table.Clear()
		s.present[i].Store(false)
		s.healthy[i].Store(true)
	}
	s.mu.Lock()
	s.held = map[Held]int{}
	s.flips++
	s.mu.Unlock()
	s.publish()
}

func (s *Snapshot) ready(ctx context.Context) bool {
	if !s.served() {
		return false
	}
	for _, k := range s.kinds() {
		ok, err := s.src.Can(ctx, "list", k.gvr.Group, k.gvr.Resource, "")
		if err != nil {
			log.Printf("desktops: review list %s: %v", k.gvr.Resource, err)
			return false
		}
		if !ok {
			return false
		}
	}
	return true
}

// Started reports whether the reflectors run: until then every read is empty
// and a frame needs no watermark.
func (s *Snapshot) Started() bool { return s.started.Load() }

func (s *Snapshot) publish() {
	if s.bus != nil {
		s.bus.Publish(eventbus.DesktopsChanged)
	}
}

// flipped moves every caller's watermark: each pull says whether it is stale.
func (s *Snapshot) flipped() {
	s.mu.Lock()
	s.flips++
	s.mu.Unlock()
	s.publish()
}

func (s *Snapshot) synced() bool {
	if !s.started.Load() || !s.instances.Synced() {
		return false
	}
	for i, k := range s.kinds() {
		if s.present[i].Load() && !k.table.Synced() {
			return false
		}
	}
	return true
}

// Base is the level of what every caller's pull reads besides rows: whether
// it is synced, and whether it is stale.
func (s *Snapshot) Base() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	level := s.flips << 1
	if s.synced() {
		level |= 1
	}
	return level
}

// Gen is the level of one kind in one namespace.
func (s *Snapshot) Gen(h Held) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gens[h]
}

// HeldIn lists the kinds that have rows in namespaces ("" for the classes),
// so a caller is reviewed only for what it could be shown.
func (s *Snapshot) HeldIn(namespaces []string) []Held {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Held
	for _, ns := range namespaces {
		for _, k := range s.kinds() {
			if h := (Held{ns, k.gvr.Resource}); s.held[h] > 0 {
				out = append(out, h)
			}
		}
	}
	return out
}

// Overview is what readable shows: each kind in the namespaces it names, and
// the classes when it holds them at "". A pool-made desktop sits under its
// pool where the pool is shown, else with the others.
func (s *Snapshot) Overview(readable map[Held]bool) model.Desktops {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := model.Desktops{
		Synced:   s.synced(),
		Stale:    !reflect.AllHealthy(s.healthy[:]),
		Classes:  []model.DesktopClass{},
		Pools:    []model.DesktopPool{},
		Desktops: []model.Desktop{},
		Claims:   []model.DesktopClaim{},
	}
	if readable[Held{Resource: GVRClass.Resource}] {
		for _, c := range s.classes.Rows {
			out.Classes = append(out.Classes, c)
		}
	}
	claimed := map[string]model.DesktopClaim{}
	for _, c := range s.claims.Rows {
		if !readable[Held{c.Namespace, GVRClaim.Resource}] {
			continue
		}
		out.Claims = append(out.Claims, c)
		if c.Desktop == "" {
			continue
		}
		key := c.Namespace + "/" + c.Desktop
		if held, ok := claimed[key]; !ok || claimFirst(c, held) < 0 {
			claimed[key] = c
		}
	}
	pools := map[string]int{}
	for key, p := range s.pools.Rows {
		if !readable[Held{p.Namespace, GVRPool.Resource}] {
			continue
		}
		pools[key] = len(out.Pools)
		p.Desktops = []model.Desktop{}
		out.Pools = append(out.Pools, p)
	}
	for key, d := range s.instances.Rows {
		if !readable[Held{d.Namespace, GVRInstance.Resource}] {
			continue
		}
		d.Claim = claimed[key].Name
		if i, ok := pools[d.Namespace+"/"+d.Pool]; ok {
			out.Pools[i].Desktops = append(out.Pools[i].Desktops, d)
			continue
		}
		out.Desktops = append(out.Desktops, d)
	}
	slices.SortFunc(out.Classes, func(a, b model.DesktopClass) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(out.Pools, func(a, b model.DesktopPool) int {
		return cmp.Or(strings.Compare(a.Namespace, b.Namespace), strings.Compare(a.Name, b.Name))
	})
	for _, p := range out.Pools {
		slices.SortFunc(p.Desktops, byDesktop)
	}
	slices.SortFunc(out.Desktops, byDesktop)
	slices.SortFunc(out.Claims, func(a, b model.DesktopClaim) int {
		return cmp.Or(strings.Compare(a.Namespace, b.Namespace), strings.Compare(a.Name, b.Name))
	})
	return out
}

func byDesktop(a, b model.Desktop) int {
	return cmp.Or(strings.Compare(a.Namespace, b.Namespace), strings.Compare(a.Name, b.Name))
}

// claimFirst orders two claims on one desktop: the one holding it, then by
// name, so the desktop names one claim whatever the map order.
func claimFirst(a, b model.DesktopClaim) int {
	bound := func(c model.DesktopClaim) int {
		if c.Phase == "Bound" {
			return 0
		}
		return 1
	}
	return cmp.Or(bound(a)-bound(b), strings.Compare(a.Name, b.Name))
}

func decode[T any](u *unstructured.Unstructured) T {
	var doc T
	// The apiserver validated the object: a field that fails to convert is
	// near impossible, and those converted before it still show the row.
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &doc)
	return doc
}

type classDoc struct {
	Spec struct {
		Image string `json:"image"`
	} `json:"spec"`
}

func decodeClass(u *unstructured.Unstructured) (model.DesktopClass, bool) {
	return model.DesktopClass{Name: u.GetName(), Image: decode[classDoc](u).Spec.Image}, true
}

// poolDoc reads each count DeskuBe leaves out as 0.
type poolDoc struct {
	Spec struct {
		ClassName  string `json:"className"`
		Seats      int32  `json:"seats"`
		Warm       int32  `json:"warm"`
		Assignment string `json:"assignment"`
		Closed     bool   `json:"closed"`
	} `json:"spec"`
	Status struct {
		model.DesktopPoolCounts `json:",inline"`
		Conditions              []model.DesktopCondition `json:"conditions"`
	} `json:"status"`
}

func decodePool(u *unstructured.Unstructured) (model.DesktopPool, bool) {
	doc := decode[poolDoc](u)
	return model.DesktopPool{
		Namespace:  u.GetNamespace(),
		Name:       u.GetName(),
		Class:      doc.Spec.ClassName,
		Seats:      doc.Spec.Seats,
		KeepWarm:   doc.Spec.Warm,
		Assignment: doc.Spec.Assignment,
		Closed:     doc.Spec.Closed,
		Counts:     doc.Status.DesktopPoolCounts,
		Conditions: doc.Status.Conditions,
	}, true
}

// instanceDoc names the fields the console shows. Not spec.pairing nor
// status.pairing: both carry a live pairing PIN. Not status.adminSecret. Not
// status.lastActive: DeskuBe rewrites it about every 30s on a busy desktop,
// and each rewrite would publish.
type instanceDoc struct {
	Spec struct {
		ClassName string `json:"className"`
		Image     string `json:"image"`
		Owner     string `json:"owner"`
		Running   *bool  `json:"running"`
	} `json:"spec"`
	Status struct {
		Phase        string                   `json:"phase"`
		Address      string                   `json:"address"`
		SessionState string                   `json:"sessionState"`
		Conditions   []model.DesktopCondition `json:"conditions"`
	} `json:"status"`
}

func decodeInstance(u *unstructured.Unstructured) (model.Desktop, bool) {
	doc := decode[instanceDoc](u)
	return model.Desktop{
		Namespace:  u.GetNamespace(),
		Name:       u.GetName(),
		Class:      doc.Spec.ClassName,
		Image:      doc.Spec.Image,
		Owner:      doc.Spec.Owner,
		Running:    doc.Spec.Running == nil || *doc.Spec.Running,
		Phase:      doc.Status.Phase,
		Address:    doc.Status.Address,
		Session:    doc.Status.SessionState,
		Pool:       madeBy(u),
		Conditions: doc.Status.Conditions,
	}, true
}

// madeBy is the pool controlling a desktop, as DeskuBe's own admission policy
// tells one: not the pool label, which an orphaned desktop keeps.
func madeBy(u *unstructured.Unstructured) string {
	for _, o := range u.GetOwnerReferences() {
		if o.Kind == "VdiPool" && strings.HasPrefix(o.APIVersion, Group+"/") && o.Controller != nil && *o.Controller {
			return o.Name
		}
	}
	return ""
}

// claimDoc reads a claim on a pool, or on one desktop by name. Its lastUsed
// moves when the claim gets its desktop, starts or stops, never on a session's
// heartbeat.
type claimDoc struct {
	Spec struct {
		PoolName    string `json:"poolName"`
		DesktopName string `json:"desktopName"`
		User        string `json:"user"`
		Running     *bool  `json:"running"`
	} `json:"spec"`
	Status struct {
		Phase       string `json:"phase"`
		DesktopName string `json:"desktopName"`
		Position    int32  `json:"position"`
		Message     string `json:"message"`
		LastUsed    string `json:"lastUsed"`
	} `json:"status"`
}

func decodeClaim(u *unstructured.Unstructured) (model.DesktopClaim, bool) {
	doc := decode[claimDoc](u)
	return model.DesktopClaim{
		Namespace: u.GetNamespace(),
		Name:      u.GetName(),
		User:      doc.Spec.User,
		Pool:      doc.Spec.PoolName,
		Desktop:   cmp.Or(doc.Status.DesktopName, doc.Spec.DesktopName),
		Running:   doc.Spec.Running == nil || *doc.Spec.Running,
		Phase:     doc.Status.Phase,
		Position:  doc.Status.Position,
		Message:   doc.Status.Message,
		LastUsed:  doc.Status.LastUsed,
	}, true
}
